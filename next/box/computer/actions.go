package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The agent's computer actions, ported from v1's (internal/computers/actions.go), one action to a call. Pointer and key
// input goes to the whole desktop through xdotool, as real X input, so it reaches any app and the checks that refuse
// synthetic clicks. The page actions use DevTools. Every action but stop starts the desktop first.

type Action string

const (
	Open       Action = "open"
	Click      Action = "click"
	Move       Action = "move"
	Drag       Action = "drag"
	Scroll     Action = "scroll"
	Key        Action = "key"
	Type       Action = "type"
	Text       Action = "text"
	Wait       Action = "wait"
	Screenshot Action = "screenshot"
	Launch     Action = "launch"
	Stop       Action = "stop"
)

// Point is a place on the desktop.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Request holds the fields of every action, as the computer tool names them; each action reads its own.
type Request struct {
	Status    string   `json:"status,omitempty"`
	URL       string   `json:"url,omitempty"`
	Selector  string   `json:"selector,omitempty"`
	Text      string   `json:"text,omitempty"`
	X         *int     `json:"x,omitempty"`
	Y         *int     `json:"y,omitempty"`
	Width     int      `json:"width,omitempty"`
	Height    int      `json:"height,omitempty"`
	Button    string   `json:"button,omitempty"`
	Double    bool     `json:"double,omitempty"`
	Keys      []string `json:"keys,omitempty"`
	HoldMS    *int     `json:"hold_ms,omitempty"`
	Path      []Point  `json:"path,omitempty"`
	StepMS    *int     `json:"step_ms,omitempty"`
	ScrollX   int      `json:"scroll_x,omitempty"`
	ScrollY   int      `json:"scroll_y,omitempty"`
	MS        *int     `json:"ms,omitempty"`
	Command   string   `json:"command,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
	Page      bool     `json:"page,omitempty"`
}

// Result is what an action returns: a line for the agent, and a PNG for a screenshot.
type Result struct {
	Text string
	PNG  []byte
}

// v1's limits.
const (
	maxDragPoints   = 200
	maxKeys         = 12
	maxText         = 10000
	maxScroll       = 100
	maxWaitMS       = 10000
	defaultWaitMS   = 1000
	defaultHoldMS   = 50
	defaultStepMS   = 10
	maxStepMS       = 500
	defaultLaunchMS = 15000
	maxLaunchMS     = 60000
)

// desktopSize is Size as numbers.
func desktopSize() (int, int) {
	w, h, _ := strings.Cut(Size, "x")
	width, _ := strconv.Atoi(w)
	height, _ := strconv.Atoi(h)
	return width, height
}

func inside(x, y int) error {
	width, height := desktopSize()
	if x < 0 || x >= width || y < 0 || y >= height {
		return fmt.Errorf("%d,%d is outside the %dx%d desktop", x, y, width, height)
	}
	return nil
}

// at reads the request's point, which needs both x and y.
func (r Request) at() (Point, bool, error) {
	if (r.X == nil) != (r.Y == nil) {
		return Point{}, false, errors.New("give both x and y, or neither")
	}
	if r.X == nil {
		return Point{}, false, nil
	}
	return Point{*r.X, *r.Y}, true, inside(*r.X, *r.Y)
}

// Validate checks a request whole before any input, as v1 checked a batch.
func Validate(action Action, r Request) error {
	if utf8.RuneCountInString(r.Status) > 80 || strings.ContainsAny(r.Status, "\r\n") {
		return errors.New("status is one line of at most 80 characters")
	}
	at, hasPoint, err := r.at()
	if err != nil {
		return err
	}
	switch action {
	case Open:
		if r.URL == "" {
			return errors.New("url is required")
		}
	case Click:
		if (r.Selector != "") == hasPoint {
			return errors.New("click takes a selector or x and y")
		}
		if r.Button != "" && buttons[r.Button] == "" {
			return errors.New("button is left, middle or right")
		}
	case Move:
		if !hasPoint {
			return errors.New("move needs x and y")
		}
	case Drag:
		if len(r.Path) < 2 || len(r.Path) > maxDragPoints {
			return fmt.Errorf("path holds 2 to %d points", maxDragPoints)
		}
		for _, p := range r.Path {
			if err := inside(p.X, p.Y); err != nil {
				return err
			}
		}
		if r.StepMS != nil && (*r.StepMS < 0 || *r.StepMS > maxStepMS) {
			return fmt.Errorf("step_ms is 0 to %d", maxStepMS)
		}
	case Scroll:
		if r.ScrollX == 0 && r.ScrollY == 0 {
			return errors.New("scroll needs scroll_x or scroll_y")
		}
		if abs(r.ScrollX) > maxScroll || abs(r.ScrollY) > maxScroll {
			return fmt.Errorf("scroll_x and scroll_y are -%d to %d notches", maxScroll, maxScroll)
		}
	case Key:
		if len(r.Keys) == 0 {
			return errors.New("keys is required")
		}
		if r.HoldMS != nil && (*r.HoldMS < 0 || *r.HoldMS > maxWaitMS) {
			return fmt.Errorf("hold_ms is 0 to %d", maxWaitMS)
		}
	case Type:
		if r.Text == "" {
			return errors.New("text is required")
		}
		if utf8.RuneCountInString(r.Text) > maxText || !utf8.ValidString(r.Text) {
			return fmt.Errorf("text is valid UTF-8 of at most %d characters", maxText)
		}
		for _, c := range r.Text {
			if unicode.IsControl(c) && c != '\n' && c != '\t' && c != '\r' {
				return errors.New("text holds a control character")
			}
		}
	case Wait:
		if r.MS != nil && (*r.MS < 0 || *r.MS > maxWaitMS) {
			return fmt.Errorf("ms is 0 to %d", maxWaitMS)
		}
	case Screenshot:
		if hasPoint != (r.Width > 0 || r.Height > 0) {
			return errors.New("a region takes x, y, width and height")
		}
		if hasPoint {
			if r.Width <= 0 || r.Height <= 0 {
				return errors.New("width and height are positive")
			}
			if err := inside(at.X+r.Width-1, at.Y+r.Height-1); err != nil {
				return errors.New("the region passes the edge of the desktop")
			}
		}
		if r.Page && hasPoint {
			return errors.New("a page screenshot takes no region")
		}
	case Launch:
		if strings.TrimSpace(r.Command) == "" {
			return errors.New("command is required")
		}
		if r.TimeoutMS < 0 || r.TimeoutMS > maxLaunchMS {
			return fmt.Errorf("timeout_ms is 0 to %d", maxLaunchMS)
		}
	case Text, Stop:
	default:
		return fmt.Errorf("unknown computer action %q", action)
	}
	if len(r.Keys) > maxKeys {
		return fmt.Errorf("keys holds at most %d names", maxKeys)
	}
	if _, err := keysymsOf(r.Keys); err != nil {
		return err
	}
	return nil
}

func abs(n int) int { return max(n, -n) }

var buttons = map[string]string{"left": "1", "middle": "2", "right": "3"}

// v1's key aliases, read in any case.
var aliases = map[string]string{"CTRL": "Control_L", "CONTROL": "Control_L", "ALT": "Alt_L", "SHIFT": "Shift_L", "META": "Super_L", "SUPER": "Super_L", "CMD": "Super_L", "ENTER": "Return", "RETURN": "Return", "ESC": "Escape", "ESCAPE": "Escape", "TAB": "Tab", "BACKSPACE": "BackSpace", "DELETE": "Delete", "SPACE": "space", "UP": "Up", "DOWN": "Down", "LEFT": "Left", "RIGHT": "Right", "HOME": "Home", "END": "End", "PAGEUP": "Page_Up", "PAGEDOWN": "Page_Down"}

// runeKeysym is a character's keysym: a Latin-1 character is its own, and any other its Unicode keysym.
func runeKeysym(c rune) uint32 {
	if c >= 0x20 && c <= 0x7e || c >= 0xa0 && c <= 0xff {
		return uint32(c)
	}
	return 0x01000000 + uint32(c)
}

// v1's names for the keys Shift turns a US key into, by the name without Shift.
var shiftedNames = map[string]string{"minus": "underscore", "equal": "plus", "bracketleft": "braceleft", "bracketright": "braceright", "backslash": "bar", "semicolon": "colon", "apostrophe": "quotedbl", "comma": "less", "period": "greater", "slash": "question", "grave": "asciitilde"}

// keysymsOf turns the request's keys into keysyms: v1's aliases, a character, or an X keysym name. Each may also be a
// chord such as ctrl+l, an improvement on v1, which took only a list. With Shift among them, v1's rule applies: a
// character is the one Shift types on a US keyboard, so shift+1 presses Shift and !, and ctrl+shift+t presses T, as
// Xvnc would otherwise let go of Shift to type a lower-case t.
func keysymsOf(keys []string) ([]uint32, error) {
	var parts []string
	for _, key := range keys {
		if utf8.RuneCountInString(key) > 1 && strings.Contains(strings.Trim(key, "+"), "+") {
			parts = append(parts, strings.Split(key, "+")...)
		} else {
			parts = append(parts, key)
		}
	}
	names := make([]string, len(parts))
	shift := false
	for i, part := range parts {
		if part == "" || utf8.RuneCountInString(part) == 1 && unicode.IsControl([]rune(part)[0]) {
			return nil, fmt.Errorf("unknown key %q", strings.Join(keys, " "))
		}
		names[i] = part
		if alias, ok := aliases[strings.ToUpper(part)]; ok {
			names[i] = alias
		}
		shift = shift || names[i] == "Shift_L" || names[i] == "Shift_R"
	}
	out := make([]uint32, len(names))
	for i, name := range names {
		if utf8.RuneCountInString(name) == 1 {
			c := []rune(name)[0]
			if shift {
				const plain, shifted = "1234567890-=[]\\;',./`", "!@#$%^&*()_+{}|:\"<>?~"
				if at := strings.IndexRune(plain, c); at >= 0 {
					c = rune(shifted[at])
				} else {
					c = unicode.ToUpper(c)
				}
			}
			out[i] = runeKeysym(c)
			continue
		}
		if shift && shiftedNames[name] != "" {
			name = shiftedNames[name]
		}
		keysym, ok := keysyms[name]
		if !ok {
			return nil, fmt.Errorf("unknown key %q", parts[i])
		}
		out[i] = keysym
	}
	return out, nil
}

// modifier reports Shift, Control, Meta, Alt, Super and Hyper, left or right, which a chord presses first.
func modifier(keysym uint32) bool {
	return keysym >= 0xffe1 && keysym <= 0xffee && keysym != 0xffe5 && keysym != 0xffe6
}

// xdotoolCommand is xdotool on the desktop, for the pointer and window lookups; keys go to Xvnc. It reads window names
// as UTF-8, which the box's C locale would refuse.
func xdotoolCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "xdotool", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+display, "LC_ALL=C.UTF-8")
	return cmd
}

// xdotool runs one chain of xdotool commands on the desktop.
func xdotool(ctx context.Context, args ...string) error {
	cmd := xdotoolCommand(ctx, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xdotool: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func seconds(ms int) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

// holding runs a pointer chain with keys held down around it, as v1 held a click's keys. The keys go down on Xvnc and
// the pointer goes through xdotool, two connections, so the keys are Xvnc's before the chain starts, and the chain's
// events get a moment to land before they go up. A failed chain still lets them go.
func holding(ctx context.Context, held []uint32, chain []string) error {
	if len(held) == 0 {
		return xdotool(ctx, chain...)
	}
	k, err := DialKeyboard(ctx, desktopSocket)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.Down(held...); err != nil {
		return err
	}
	if err := k.Sync(ctx); err != nil {
		return err
	}
	err = xdotool(ctx, chain...)
	time.Sleep(50 * time.Millisecond)
	if up := k.Up(reversed(held)...); err == nil {
		err = up
	}
	if synced := k.Sync(ctx); err == nil {
		err = synced
	}
	return err
}

func reversed(keysyms []uint32) []uint32 {
	out := slices.Clone(keysyms)
	slices.Reverse(out)
	return out
}

// desktopSocket is the display's VNC socket, which the actions read the screen and send keys through.
var desktopSocket = VNCSocket

// Agent reaches the box agent's computer endpoint, which owns the desktop.
type Agent struct{ client *http.Client }

func NewAgent(socket string) Agent {
	return Agent{client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}

// Started is the endpoint's answer: DevTools, a report of an exit since the last start, and a launch's log.
type Started struct {
	URL  string `json:"url"`
	Exit string `json:"exit"`
	Log  string `json:"log"`
}

func (a Agent) post(ctx context.Context, path string, body any) (Started, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna"+path, bytes.NewReader(data))
	if err != nil {
		return Started{}, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return Started{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var text bytes.Buffer
		_, _ = text.ReadFrom(resp.Body)
		return Started{}, fmt.Errorf("computer: %s", strings.TrimSpace(text.String()))
	}
	var started Started
	return started, json.NewDecoder(resp.Body).Decode(&started)
}

// Start starts the desktop, or the parts of it that stopped.
func (a Agent) Start(ctx context.Context) (Started, error) {
	return a.post(ctx, "/computer", StartRequest{})
}

// Do runs one action. The text of its result starts with a report of anything of the desktop that exited since.
func Do(ctx context.Context, agent Agent, action Action, r Request) (Result, error) {
	if err := Validate(action, r); err != nil {
		return Result{}, err
	}
	if action == Stop {
		if _, err := agent.post(ctx, "/computer/stop", nil); err != nil {
			return Result{}, err
		}
		return Result{Text: "The desktop stopped. The next action starts it again."}, nil
	}
	started, err := agent.Start(ctx)
	if err != nil {
		return Result{}, err
	}
	result, err := run(ctx, agent, started.URL, action, r)
	if started.Exit != "" {
		result.Text = strings.TrimSpace(started.Exit + ". " + result.Text)
	}
	return result, err
}

func run(ctx context.Context, agent Agent, base string, action Action, r Request) (Result, error) {
	page := func(do func(*DevTools) (string, error)) (Result, error) {
		d, err := Connect(ctx, base)
		if err != nil {
			return Result{}, err
		}
		defer d.Close()
		text, err := do(d)
		return Result{Text: text}, err
	}
	switch action {
	case Open:
		return page(func(d *DevTools) (string, error) {
			title, err := navigate(ctx, d, r.URL)
			return fmt.Sprintf("Loaded the page %q.", strings.TrimSpace(title)), err
		})
	case Text:
		return page(func(d *DevTools) (string, error) { return pageText(ctx, d, r.Selector) })
	case Wait:
		ms := defaultWaitMS
		if r.MS != nil {
			ms = *r.MS
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
			return Result{Text: fmt.Sprintf("Waited %d ms.", ms)}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	case Screenshot:
		if r.Page {
			d, err := Connect(ctx, base)
			if err != nil {
				return Result{}, err
			}
			defer d.Close()
			shot, err := pageScreenshot(ctx, d)
			return Result{PNG: shot, Text: "The whole page in Chromium."}, err
		}
		width, height := desktopSize()
		region, text := Rect{0, 0, width, height}, fmt.Sprintf("The %s desktop.", Size)
		if at, ok, _ := r.at(); ok {
			region = Rect{at.X, at.Y, r.Width, r.Height}
			text = fmt.Sprintf("%s of the %s desktop; add %d,%d to a point in it for the desktop's.", region, Size, at.X, at.Y)
		}
		shot, err := Capture(ctx, desktopSocket, region)
		return Result{PNG: shot, Text: text}, err
	case Launch:
		return launch(ctx, agent, r)
	}
	// Input: watch the screen from before it, and say what changed once it settles. v1 waited for damage after a click
	// or a shortcut only; every input waits here, since what changed is the answer.
	changes, watchErr := WatchChanges(ctx, desktopSocket)
	if watchErr == nil {
		defer changes.Close()
	}
	text, err := input(ctx, base, action, r)
	if err != nil || action == Move {
		return Result{Text: text}, err
	}
	if watchErr != nil {
		time.Sleep(200 * time.Millisecond) // v1's fixed pause when damage was unavailable
		return Result{Text: text}, nil
	}
	settled, err := changes.Settle(ctx, time.Second, 200*time.Millisecond, 3*time.Second)
	if err != nil {
		return Result{Text: text}, nil
	}
	return Result{Text: strings.TrimSpace(text + " " + settled.Describe())}, nil
}

// input sends one pointer or key action.
func input(ctx context.Context, base string, action Action, r Request) (string, error) {
	keys, _ := keysymsOf(r.Keys)
	at, _, _ := r.at()
	switch action {
	case Click:
		if r.Selector != "" {
			d, err := Connect(ctx, base)
			if err != nil {
				return "", err
			}
			x, y, err := elementPoint(ctx, d, r.Selector)
			d.Close()
			if err != nil {
				return "", err
			}
			if err := inside(x, y); err != nil {
				return "", fmt.Errorf("the element is off the screen: %w", err)
			}
			at = Point{x, y}
		}
		button := buttons[r.Button]
		if button == "" {
			button = "1"
		}
		chain := []string{"mousemove", strconv.Itoa(at.X), strconv.Itoa(at.Y), "click"}
		if r.Double {
			chain = append(chain, "--repeat", "2", "--delay", "80")
		}
		if err := holding(ctx, keys, append(chain, button)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Clicked at %d,%d.", at.X, at.Y), nil
	case Move:
		return fmt.Sprintf("Moved to %d,%d.", at.X, at.Y), xdotool(ctx, "mousemove", strconv.Itoa(at.X), strconv.Itoa(at.Y))
	case Drag:
		step := defaultStepMS
		if r.StepMS != nil {
			step = *r.StepMS
		}
		chain := []string{"mousemove", strconv.Itoa(r.Path[0].X), strconv.Itoa(r.Path[0].Y), "mousedown", "1"}
		// v1: with step_ms, the press also lasts a step, so a frame-driven app sees the button held before the first move.
		if r.StepMS != nil {
			chain = append(chain, "sleep", seconds(step))
		}
		for _, p := range r.Path[1:] {
			chain = append(chain, "mousemove", strconv.Itoa(p.X), strconv.Itoa(p.Y), "sleep", seconds(step))
		}
		err := holding(ctx, keys, append(chain, "mouseup", "1"))
		if err != nil {
			_ = xdotool(context.WithoutCancel(ctx), "mouseup", "1")
			return "", err
		}
		end := r.Path[len(r.Path)-1]
		return fmt.Sprintf("Dragged from %d,%d to %d,%d.", r.Path[0].X, r.Path[0].Y, end.X, end.Y), nil
	case Scroll:
		width, height := desktopSize()
		if _, ok, _ := r.at(); !ok {
			at = Point{width / 2, height / 2}
		}
		chain := []string{"mousemove", strconv.Itoa(at.X), strconv.Itoa(at.Y)}
		for _, axis := range []struct{ n, less, more int }{{r.ScrollY, 4, 5}, {r.ScrollX, 6, 7}} {
			if axis.n == 0 {
				continue
			}
			button := axis.more
			if axis.n < 0 {
				button = axis.less
			}
			chain = append(chain, "click", "--repeat", strconv.Itoa(abs(axis.n)), "--delay", "20", strconv.Itoa(button))
		}
		return fmt.Sprintf("Scrolled at %d,%d.", at.X, at.Y), holding(ctx, keys, chain)
	case Key:
		hold := defaultHoldMS
		if r.HoldMS != nil {
			hold = *r.HoldMS
		}
		// v1 pressed a chord's modifiers before its other keys, and let them go last.
		var chord []uint32
		for _, k := range keys {
			if modifier(k) {
				chord = append(chord, k)
			}
		}
		for _, k := range keys {
			if !modifier(k) {
				chord = append(chord, k)
			}
		}
		return "Pressed " + strings.Join(r.Keys, " ") + ".", press(ctx, chord, time.Duration(hold)*time.Millisecond)
	case Type:
		if r.Selector != "" {
			d, err := Connect(ctx, base)
			if err != nil {
				return "", err
			}
			defer d.Close()
			return fmt.Sprintf("Typed %d characters into %s.", utf8.RuneCountInString(r.Text), r.Selector), pageInsert(ctx, d, r.Selector, r.Text)
		}
		return fmt.Sprintf("Typed %d characters.", utf8.RuneCountInString(r.Text)), TypeText(ctx, desktopSocket, r.Text)
	}
	return "", fmt.Errorf("unknown computer action %q", action)
}

// press holds a chord's keys down for hold, from when Xvnc has taken them, then lets them go in reverse.
func press(ctx context.Context, chord []uint32, hold time.Duration) error {
	k, err := DialKeyboard(ctx, desktopSocket)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.Down(chord...); err != nil {
		return err
	}
	if err := k.Sync(ctx); err != nil {
		return err
	}
	select {
	case <-time.After(hold):
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := k.Up(reversed(chord)...); err != nil {
		return err
	}
	return k.Sync(ctx)
}

// windows lists the desktop's visible windows.
func windows(ctx context.Context) (map[string]bool, error) {
	// Only named windows: jwm's frames around them have no name.
	out, err := xdotoolCommand(ctx, "search", "--onlyvisible", "--name", ".").Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && len(out) == 0) { // no window at all exits 1
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range strings.Fields(string(out)) {
		seen[id] = true
	}
	return seen, nil
}

// launch starts a command on the desktop and waits for its window, as v1's open did.
func launch(ctx context.Context, agent Agent, r Request) (Result, error) {
	before, err := windows(ctx)
	if err != nil {
		return Result{}, err
	}
	started, err := agent.post(ctx, "/computer/launch", StartRequest{Launch: r.Command})
	if err != nil {
		return Result{}, err
	}
	timeout := defaultLaunchMS
	if r.TimeoutMS > 0 {
		timeout = r.TimeoutMS
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		now, err := windows(ctx)
		if err != nil {
			return Result{}, err
		}
		for id := range now {
			if before[id] {
				continue
			}
			name, _ := xdotoolCommand(ctx, "getwindowname", id).Output()
			return Result{Text: fmt.Sprintf("Opened %q, window %s. Its output goes to %s.", strings.TrimSpace(string(name)), id, started.Log)}, nil
		}
	}
	return Result{}, fmt.Errorf("no new window in %d ms; it may still be starting or have no window, and its output goes to %s", timeout, started.Log)
}
