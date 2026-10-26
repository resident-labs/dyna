//go:build linux

package computers

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Read dimensions from the frame itself. Desktop dimensions can change while
// last.png remains the last captured image, including across a daemon restart.
func readShotSize(path string) (Size, error) {
	f, err := os.Open(path)
	if err != nil {
		return Size{}, err
	}
	defer f.Close()
	dimensions, err := png.DecodeConfig(f)
	if err != nil {
		return Size{}, err
	}
	return Size{Width: dimensions.Width, Height: dimensions.Height}, nil
}

func (s *supervisor) capture(fresh, frame bool) (Shot, error) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	last := filepath.Join(Dir(s.meta.SessionID), "last.png")
	s.mu.Lock()
	comp := s.compositor
	if !comp.alive() {
		shot := s.shot
		shot.Path = ""
		shot.Message = "no windows yet"
		if _, err := os.Stat(last); err == nil {
			shot.Path = last
		}
		s.mu.Unlock()
		return shot, nil
	}
	if !fresh && !frame && !s.shot.CapturedAt.IsZero() && s.shot.Width == s.state.Width && s.shot.Height == s.state.Height {
		shot := s.shot
		s.mu.Unlock()
		return shot, nil
	}
	env := s.desktopEnv()
	width, height := s.state.Width, s.state.Height
	s.mu.Unlock()
	tmp := last + ".tmp"
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "grim", "-t", "png", tmp)
	cmd.Env = env
	if out, err := combinedOwned(cmd); err != nil {
		return Shot{}, fmt.Errorf("capture: %w: %s", err, out)
	}
	defer os.Remove(tmp)
	size, err := readShotSize(tmp)
	if err != nil {
		return Shot{}, err
	}
	if size.Width != width || size.Height != height {
		return Shot{}, fmt.Errorf("output size changed: expected %dx%d, got %dx%d", width, height, size.Width, size.Height)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.compositor != comp || !comp.alive() {
		return Shot{}, fmt.Errorf("computer stopped during capture")
	}
	now := time.Now()
	shot := Shot{Path: last, CapturedAt: now, Width: width, Height: height}
	if err = os.Rename(tmp, last); err != nil {
		return Shot{}, err
	}
	s.shot = shot
	s.state.ShotCapturedAt = now
	if frame {
		dir := filepath.Join(Dir(s.meta.SessionID), "frames")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return Shot{}, err
		}
		shot.Path = filepath.Join(dir, fmt.Sprintf("%d.png", now.UnixNano()))
		b, err := os.ReadFile(last)
		if err != nil {
			return Shot{}, err
		}
		if err = os.WriteFile(shot.Path, b, 0600); err != nil {
			return Shot{}, err
		}
	}
	s.record("shot", &shot)
	return shot, nil
}
func (s *supervisor) agentTarget(ctx context.Context) (comp *process, env []string, display string, width, height int, err error) {
	for {
		s.mu.Lock()
		if !s.compositor.alive() {
			s.mu.Unlock()
			return nil, nil, "", 0, 0, nil
		}
		if s.state.Control == ControlAgent {
			comp = s.compositor
			env = s.desktopEnv()
			display = s.privateWayland
			width, height = s.state.Width, s.state.Height
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			err = fmt.Errorf("human has control: %w", ctx.Err())
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (s *supervisor) act(ctx context.Context, r Request) (ActionResult, error) {
	intent, err := ActionStatus(r.Actions, r.Status)
	if err != nil {
		return ActionResult{}, err
	}

	select {
	case s.inputGate <- struct{}{}:
		defer func() { <-s.inputGate }()
	case <-ctx.Done():
		return ActionResult{}, ctx.Err()
	}
	s.mu.Lock()
	err = ValidateActions(r.Actions, s.state.Width, s.state.Height)
	s.mu.Unlock()
	if err != nil {
		return ActionResult{}, err
	}
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	if timeout < 0 {
		return ActionResult{}, fmt.Errorf("timeout must be positive")
	}
	if intent != "" {
		s.mu.Lock()
		s.recordIntent(intent)
		s.mu.Unlock()
	}
	waitctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	result := ActionResult{Frames: []Frame{}}
	var pointer *guardedPointer
	var rawPointer *virtualPointer
	defer func() {
		s.mu.Lock()
		s.actPointer = nil
		s.mu.Unlock()
		if pointer != nil {
			pointer.release()
			rawPointer.close()
		}
	}()
	var keyboard *virtualKeyboard
	var generation *process
	explicit := false
	for i, a := range r.Actions {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		comp, _, display, width, height, err := s.agentTarget(waitctx)
		if err != nil {
			return result, err
		}
		if comp == nil {
			shot, err := s.capture(false, false)
			result.Shot = shot
			return result, err
		}
		if generation != nil && generation != comp {
			return result, fmt.Errorf("computer restarted during actions")
		}
		generation = comp
		kind := actionType(a)
		keys, _ := actionValue[[]string](a, "keys")
		if keyboard == nil && (kind == "type" || len(keys) > 0) {
			keyboard, err = s.inputKeyboard(ctx, comp, display, r.Actions)
			if err != nil {
				return result, err
			}

		}
		switch kind {
		case "click", "double_click", "move", "drag", "scroll", "pointer_down", "pointer_up":
			if pointer == nil {
				raw, err := newVirtualPointer(ctx, display, width, height)
				if err != nil {
					return result, err
				}
				rawPointer, pointer = raw, newGuardedPointer(raw)
				s.mu.Lock()
				s.actPointer = pointer
				s.mu.Unlock()
			}
		}
		if kind == "screenshot" {
			explicit = true
			shot, err := s.capture(true, true)
			if err != nil {
				return result, err
			}
			index := i
			result.Frames = append(result.Frames, Frame{Shot: shot, ActionIndex: &index, OffsetMS: time.Since(started).Milliseconds()})
			result.Shot = shot
		} else if err := s.runInputAction(ctx, a, pointer, keyboard, display); err != nil {
			return result, fmt.Errorf("action %d: %w", i, err)
		}
		result.ActionsExecuted++
		redacted := EventAction(a)
		s.mu.Lock()
		s.lastActivity = time.Now()
		event := Event{Type: "act", SessionID: s.meta.SessionID, TS: time.Now(), Status: statusCopy(s.state), Action: redacted}
		s.mu.Unlock()
		if s.emit != nil {
			s.emit(event)
		}
		f, err := os.OpenFile(EventsPath(s.meta.SessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_ = json.NewEncoder(f).Encode(event)
			f.Close()
		}
	}
	if !explicit {
		shot, err := s.capture(true, true)
		if err != nil {
			return result, err
		}
		result.Shot = shot
		result.Frames = append(result.Frames, Frame{Shot: shot, OffsetMS: time.Since(started).Milliseconds()})
	}
	return result, nil
}

// runInputAction runs one input action under its own cancel, so a takeover
// during a held gesture ends the action's waits at once.
func (s *supervisor) runInputAction(ctx context.Context, a Action, p *guardedPointer, k *virtualKeyboard, display string) error {
	if p == nil {
		return executeInputAction(ctx, a, nil, k, display)
	}
	actCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.begin(cancel)
	return p.end(executeInputAction(actCtx, a, p, k, display))
}

// A compositor roundtrip acknowledges input, not the application's focus work.
// Damage after a click or modifier shortcut lets the application redraw
// before later input. Other windows can also damage the output; this is a
// bounded scheduling aid, not an application-level acknowledgement. It fails
// open: without damage, a fixed pause stands in and the action still succeeds.
func executeInputAction(ctx context.Context, a Action, p *guardedPointer, k *virtualKeyboard, display string) error {
	var damage *outputDamage
	var damageErr error
	waitForFocus := isShortcut(a) || actionType(a) == "click" || actionType(a) == "double_click"
	if waitForFocus {
		damage, damageErr = newOutputDamage(ctx, display)
		if damageErr == nil {
			defer damage.close()
		}
	}
	if err := executeAction(ctx, a, p, k); err != nil {
		return err
	}
	if !waitForFocus {
		return nil
	}
	if damageErr == nil {
		// 150 ms of quiet spans more than one frame at the unwatched 10 Hz.
		damageErr = damage.settle(ctx, 500*time.Millisecond, 150*time.Millisecond)
	}
	return shortcutSettle(ctx, damageErr)
}

// shortcutFallback is the pause after a shortcut when output damage fails.
const shortcutFallback = 200 * time.Millisecond

// shortcutSettle ends a shortcut's wait. The keys were already sent, so a
// damage error only falls back to a fixed pause; cancellation still wins.
func shortcutSettle(ctx context.Context, damageErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if damageErr == nil {
		return nil
	}
	return inputPause(ctx, shortcutFallback)
}

// isShortcut reports a keypress that holds a modifier with an ordinary key.
func isShortcut(a Action) bool {
	if actionType(a) != "keypress" {
		return false
	}
	keys, _ := actionValue[[]string](a, "keys")
	modified, ordinary := false, false
	for _, key := range keys {
		if keyboardModifier(key) != 0 {
			modified = true
		} else {
			ordinary = true
		}
	}
	return modified && ordinary
}

// idleRefreshHz is the lowest output refresh rate: an unwatched desktop drops to
// it. keypressHold lasts one and a half frames at that rate, so every frame
// sees the key down.
const (
	idleRefreshHz = 10
	keypressHold  = time.Second * 3 / (2 * idleRefreshHz)
)

func executeAction(ctx context.Context, a Action, p *guardedPointer, keyboard *virtualKeyboard) (err error) {
	kind := actionType(a)
	keys, _ := actionValue[[]string](a, "keys")
	if kind == "keypress" {
		keys = shiftedKeys(keys)
	}
	if kind == "keypress" || kind == "type" || len(keys) > 0 {
		if err := keyboard.activate(); err != nil {
			return err
		}
		if err := inputPause(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
	// Release keys on cancellation and pointer failures as well as success.
	var pressed []string
	defer func() {
		for i := len(pressed) - 1; i >= 0; i-- {
			if releaseErr := keyboard.key(pressed[i], false); err == nil {
				err = releaseErr
			}
		}
	}()
	for _, key := range keys {
		if err := keyboard.key(key, true); err != nil {
			return err
		}
		pressed = append(pressed, key)
	}
	if kind != "keypress" && len(keys) > 0 {
		if err := inputPause(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
	x, _ := actionValue[int](a, "x")
	y, _ := actionValue[int](a, "y")
	switch kind {
	case "move":
		return p.move(x, y)
	case "click", "double_click":
		if err := p.move(x, y); err != nil {
			return err
		}
		button := "left"
		count := 1
		if kind == "click" {
			button, _ = actionValue[string](a, "button")
		} else {
			count = 2
		}
		for i := 0; i < count; i++ {
			if err := p.button(button, true); err != nil {
				return err
			}
			if err := p.button(button, false); err != nil {
				return err
			}
			if count == 2 && i == 0 {
				time.Sleep(80 * time.Millisecond)
			}
		}
	case "drag":
		path, _ := actionValue[[]point](a, "path")
		step, paced, err := dragStep(a)
		if err != nil {
			return err
		}
		return dragPath(ctx, p, path, step, paced, inputPause)
	case "pointer_down", "pointer_up":
		return pointerButtonAction(p, a)
	case "scroll":
		if err := p.move(x, y); err != nil {
			return err
		}
		sx, _ := actionValue[int](a, "scroll_x")
		sy, _ := actionValue[int](a, "scroll_y")
		return p.scroll(sx, sy)
	case "keypress":
		// Hold the keys for a moment. An app that samples key state once per
		// frame misses a press that is released within the same frame.
		return inputPause(ctx, keypressHold)
	case "type":
		text, _ := actionValue[string](a, "text")
		return keyboard.typeText(ctx, text)
	case "wait":
		duration, err := waitDuration(a)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(duration):
		}
	case "screenshot":
	default:
		return fmt.Errorf("unsupported action %q", kind)
	}
	return nil
}

// Resolve Shift explicitly for submitted symbols, including custom one-level
// bindings, while retaining the modifier mask for shortcut handling.
func shiftedKeys(keys []string) []string {
	shift := false
	for _, key := range keys {
		if name := keyName(key); name == "Shift_L" || name == "Shift_R" {
			shift = true
		}
	}
	if !shift {
		return keys
	}
	out := append([]string(nil), keys...)
	named := map[string]string{"minus": "underscore", "equal": "plus", "bracketleft": "braceleft", "bracketright": "braceright", "backslash": "bar", "semicolon": "colon", "apostrophe": "quotedbl", "comma": "less", "period": "greater", "slash": "question", "grave": "asciitilde"}
	plain := "1234567890-=[]\\;',./`"
	upper := "!@#$%^&*()_+{}|:\"<>?~"
	for i, key := range out {
		if utf8.RuneCountInString(key) == 1 {
			r := []rune(key)[0]
			if n := strings.IndexRune(plain, r); n >= 0 {
				out[i] = string(upper[n])
			} else {
				out[i] = string(unicode.ToUpper(r))
			}
		} else if mapped, ok := named[key]; ok {
			out[i] = mapped
		}
	}
	return out
}

// interruptActLocked is the takeover: the running batch's held buttons are
// released and its gesture stops now. The caller holds s.mu.
func (s *supervisor) interruptActLocked() {
	if s.actPointer != nil {
		s.actPointer.interrupt()
	}
}
