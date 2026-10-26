//go:build linux

package computers

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Keep the keyboard alive after input. GTK clients have a separate Wayland
// connection for their keymap and can decode input after the sender returns.
// Destroying the keyboard immediately restores a different seat keymap.
type virtualKeyboard struct {
	w             *wayland
	object        uint32
	codes         map[string]uint32
	keymap        string
	modifiers     uint32
	autoShift     int
	sentModifiers uint32
	failed        bool
}

// Use defined evdev positions. Chromium discards events for unmapped physical
// codes, including gaps in the Linux numbering. These are the positions in
// Chromium's ui/events/keycodes/dom/dom_code_data.inc (152.0.7977.82).
var keyboardCodes = []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 96, 97, 98, 99, 100, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 113, 114, 115, 116, 117, 118, 119, 120, 121, 122, 123, 124, 125, 126, 127, 128, 129, 131, 132, 133, 134, 135, 136, 137, 138, 140, 142, 143, 144, 150, 152, 155, 156, 158, 159, 161, 163, 164, 165, 166, 167, 168, 169, 171, 172, 173, 174, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191, 192, 193, 194, 201, 204, 206, 207, 208, 209, 210, 217, 224, 225, 227, 228, 229, 230, 231, 232, 233, 234, 235, 244, 248, 358, 362, 370, 372, 374, 392, 397, 405, 410, 411, 418, 419, 421, 423, 429, 431, 432, 433, 464, 579, 580, 581, 582, 583, 584, 585, 586, 589, 590, 591, 592, 593, 633}

// reservedKeyboardCode reports a position that holds a key of the standard US
// layout (main block, keypad, function and navigation keys, modifiers). Extra
// symbols must not rebind these: an app that reads physical keys, such as
// Ctrl or a keypad key, would see the extra symbol instead.
func reservedKeyboardCode(code uint32) bool {
	switch {
	case code >= 1 && code <= 83, code == 87, code == 88, code >= 96 && code <= 111, code == 119, code >= 125 && code <= 127:
		return true
	}
	return false
}

var baseKeyboardCodes, baseKeyboardShift = func() (map[string]uint32, map[string]bool) {
	codes := map[string]uint32{}
	shifted := map[string]bool{}
	rows := []struct {
		plain, upper string
		start        uint32
	}{
		{"1234567890-=", "!@#$%^&*()_+", 2},
		{"qwertyuiop[]", "QWERTYUIOP{}", 16},
		{"asdfghjkl;'", "ASDFGHJKL:\"", 30},
		{"`", "~", 41}, {"\\", "|", 43}, {"zxcvbnm,./", "ZXCVBNM<>?", 44}, {" ", " ", 57},
	}
	for _, row := range rows {
		for i, r := range row.plain {
			codes[textKey(r)] = row.start + uint32(i)
		}
		for i, r := range row.upper {
			name := textKey(r)
			codes[name] = row.start + uint32(i)
			shifted[name] = r != ' '
		}
	}
	for name, code := range map[string]uint32{"Return": 28, "Escape": 1, "Tab": 15, "BackSpace": 14, "Delete": 111, "space": 57, "Up": 103, "Down": 108, "Left": 105, "Right": 106, "Home": 102, "End": 107, "Page_Up": 104, "Page_Down": 109, "Insert": 110, "Print": 99, "Pause": 119, "Caps_Lock": 58, "Num_Lock": 69, "Menu": 127} {
		codes[name] = code
	}
	for n := 1; n <= 10; n++ {
		codes[fmt.Sprintf("F%d", n)] = uint32(58 + n)
	}
	codes["F11"], codes["F12"] = 87, 88
	return codes, shifted
}()

func keyboardMap(actions []Action, previous map[string]uint32) (string, map[string]uint32, error) {
	codes := map[string]uint32{}
	used := map[uint32]bool{}
	for name, code := range baseKeyboardCodes {
		codes[name] = code
		used[code] = true
	}
	for name, code := range previous {
		codes[name] = code
		used[code] = true
	}
	var full bool
	add := func(name string) {
		if _, ok := codes[name]; ok {
			return
		}
		for _, code := range keyboardCodes {
			if !used[code] && !reservedKeyboardCode(code) {
				codes[name] = code
				used[code] = true
				return
			}
		}
		full = true
	}
	for _, a := range actions {
		for _, key := range actionKeys(a) {
			if keyboardModifier(key) == 0 {
				add(keyName(key))
			}
		}
		if actionType(a) == "type" {
			text, _ := actionValue[string](a, "text")
			for _, r := range normalizedText(text) {
				add(textKey(r))
			}
		}
	}
	if full {
		return "", nil, fmt.Errorf("computer keyboard has no free physical keycodes")
	}
	names := []string{}
	for name := range codes {
		if _, base := baseKeyboardCodes[name]; !base {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return codes[names[i]] < codes[names[j]] })
	var b strings.Builder
	b.WriteString("xkb_keymap { xkb_keycodes \"dyna\" { include \"evdev+aliases(qwerty)\"\n")
	for _, name := range names {
		code := codes[name]
		fmt.Fprintf(&b, "<K%d>=%d;\n", code, code+8)
	}
	b.WriteString("}; xkb_types \"dyna\" { include \"complete\" }; xkb_compatibility \"dyna\" { include \"complete\" }; xkb_symbols \"dyna\" { include \"pc+us+inet(evdev)\"\n")
	for _, name := range names {
		fmt.Fprintf(&b, "key <K%d> { type[Group1]=\"ONE_LEVEL\", [%s]};\n", codes[name], name)
	}
	b.WriteString("}; };\x00")
	return b.String(), codes, nil
}

func newVirtualKeyboard(ctx context.Context, display, keymap string, codes map[string]uint32) (*virtualKeyboard, error) {
	w, err := connectWayland(ctx, display)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			w.conn.Close()
		}
	}()
	seat, manager, object := w.id(), w.id(), w.id()
	if err := w.bind("wl_seat", seat, 1); err != nil {
		return nil, err
	}
	if err := w.bind("zwp_virtual_keyboard_manager_v1", manager, 1); err != nil {
		return nil, err
	}
	if err := w.send(manager, 0, wlWords(seat, object)); err != nil {
		return nil, err
	}
	k := &virtualKeyboard{w: w, object: object, codes: codes, keymap: keymap}
	if err := k.upload(keymap); err != nil {
		return nil, err
	}
	success = true
	return k, nil
}

// prepare only adds symbols. Delayed input must retain the same meaning even
// when a client has not received the newest map through its GTK connection.
func (k *virtualKeyboard) prepare(actions []Action) error {
	keymap, codes, err := keyboardMap(actions, k.codes)
	if err != nil {
		return err
	}
	if keymap == k.keymap {
		// Also publish unchanged bindings for a newly focused application.
		return k.upload(keymap)
	}
	if err := k.upload(keymap); err != nil {
		return err
	}
	k.keymap, k.codes = keymap, codes
	return nil
}
func (k *virtualKeyboard) upload(keymap string) (err error) {
	defer func() {
		if err != nil {
			k.failed = true
			k.close()
		}
	}()
	k.w.conn.SetDeadline(time.Now().Add(2 * time.Second))
	fd, err := unix.MemfdCreate("dyna-keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "dyna-keymap")
	defer file.Close()
	if _, err := file.WriteString(keymap); err != nil {
		return err
	}
	// The fd is ancillary data, not a word in the Wayland message body.
	message := append(wlWords(k.object, uint32(16)<<16), wlWords(1, uint32(len(keymap)))...)
	n, _, err := k.w.conn.WriteMsgUnix(message, unix.UnixRights(fd), nil)
	if err != nil {
		return err
	}
	if n != len(message) {
		return fmt.Errorf("short keyboard keymap write")
	}
	if err := k.w.roundtrip(); err != nil {
		return err
	}
	return nil
}

func (k *virtualKeyboard) close() { k.w.conn.Close() }
func (k *virtualKeyboard) send(op uint16, args ...uint32) (err error) {
	// Delivery is uncertain after an I/O failure. Disconnect to release held
	// state, and recreate the device with the same bindings on the next batch.
	defer func() {
		if err != nil {
			k.failed = true
			k.close()
		}
	}()
	k.w.conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := k.w.send(k.object, op, wlWords(args...)); err != nil {
		return err
	}
	return k.w.roundtrip()
}
func keyboardModifier(key string) uint32 {
	return map[string]uint32{"Shift_L": 1, "Shift_R": 1, "Control_L": 4, "Control_R": 4, "Alt_L": 8, "Alt_R": 128, "Super_L": 64, "Super_R": 64, "Meta_L": 64, "Meta_R": 64}[keyName(key)]
}
func (k *virtualKeyboard) syncModifiers() error {
	mask := k.modifiers
	if k.autoShift > 0 {
		mask |= 1
	}
	if mask == k.sentModifiers {
		return nil
	}
	if err := k.send(2, mask, 0, 0, 0); err != nil {
		return err
	}
	k.sentModifiers = mask
	return nil
}
func (k *virtualKeyboard) key(key string, pressed bool) error {
	if mask := keyboardModifier(key); mask != 0 {
		if pressed {
			k.modifiers |= mask
		} else {
			k.modifiers &^= mask
		}
		return k.syncModifiers()
	}
	name := keyName(key)
	code, ok := k.codes[name]
	if !ok {
		return fmt.Errorf("key %q missing from keyboard map", key)
	}
	shifted := baseKeyboardShift[name]
	if pressed && shifted {
		k.autoShift++
		if err := k.syncModifiers(); err != nil {
			return err
		}
	}
	state := uint32(0)
	if pressed {
		state = 1
	}
	if err := k.send(1, 0, code, state); err != nil {
		return err
	}
	if !pressed && shifted {
		k.autoShift--
		return k.syncModifiers()
	}
	return nil
}

func normalizedText(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}
func textKey(r rune) string {
	switch r {
	case '\n':
		return "Return"
	case '\t':
		return "Tab"
	}
	return keyName(string(r))
}
func inputPause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (k *virtualKeyboard) typeText(ctx context.Context, text string) error {
	for _, r := range normalizedText(text) {
		key := textKey(r)
		if err := k.key(key, true); err != nil {
			return err
		}
		// Match wtype's normal key hold and release timing, without extra pacing.
		err := inputPause(ctx, 2*time.Millisecond)
		releaseErr := k.key(key, false)
		if err != nil {
			return err
		}
		if releaseErr != nil {
			return releaseErr
		}
		if err := inputPause(ctx, 2*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// A reserved, unmapped code selects this device on the seat before real input.
// Modifier-only notifications do not select a keyboard in labwc. This lets a
// client receive the agent map before the first key after human control.
func (k *virtualKeyboard) activate() error {
	if err := k.send(1, 0, 0, 1); err != nil {
		return err
	}
	return k.send(1, 0, 0, 0)
}

func (s *supervisor) inputKeyboard(ctx context.Context, comp *process, display string, actions []Action) (*virtualKeyboard, error) {
	s.mu.Lock()
	keyboard := s.keyboard
	valid := s.compositor == comp
	s.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("computer restarted during actions")
	}
	if keyboard == nil || keyboard.failed {
		var previous map[string]uint32
		if keyboard != nil {
			previous = keyboard.codes
		}
		keymap, codes, err := keyboardMap(actions, previous)
		if err != nil {
			return nil, err
		}
		next, err := newVirtualKeyboard(ctx, display, keymap, codes)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		valid = s.compositor == comp && s.keyboard == keyboard
		if valid {
			s.keyboard = next
		}
		s.mu.Unlock()
		if !valid {
			next.close()
			return nil, fmt.Errorf("computer restarted during actions")
		}
		if keyboard != nil {
			keyboard.close()
		}
		return next, nil
	}
	// inputGate owns map updates. Do not hold the status/control lock while
	// waiting for a compositor roundtrip; stop may close this connection.
	if err := keyboard.prepare(actions); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid = s.compositor == comp && s.keyboard == keyboard
	s.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("computer restarted during actions")
	}
	return keyboard, nil
}
