package computers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// UnmarshalJSON reads a point from the object form the guide and the action
// schema show, {"x":80,"y":46}, or from a two-element array, [80, 46], so a batch
// written either way runs.
func (p *point) UnmarshalJSON(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// Pointers, so a null coordinate is refused instead of decoding as 0.
		var pair []*int
		if err := json.Unmarshal(trimmed, &pair); err != nil {
			return err
		}
		if len(pair) != 2 || pair[0] == nil || pair[1] == nil {
			return fmt.Errorf("a point array must hold exactly x and y")
		}
		p.X, p.Y = *pair[0], *pair[1]
		return nil
	}
	var named struct {
		X *int `json:"x"`
		Y *int `json:"y"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&named); err != nil {
		return err
	}
	if named.X == nil || named.Y == nil {
		return fmt.Errorf("a point needs exactly x and y")
	}
	p.X, p.Y = *named.X, *named.Y
	return nil
}

func actionValue[T any](a Action, key string) (v T, err error) {
	raw, ok := a[key]
	if !ok || string(raw) == "null" {
		return v, fmt.Errorf("%s is required", key)
	}
	err = json.Unmarshal(raw, &v)
	if err != nil {
		return v, fmt.Errorf("invalid %s: %w", key, err)
	}
	return
}

// EventTextMax is how many characters of typed text an action event keeps.
const EventTextMax = 24

const (
	maxActions     = 50
	maxWaits       = 5
	maxScreenshots = 10
	maxDragPoints  = 200
	maxScroll      = 3000
	maxKeys        = 12
	maxText        = 10000
)

const defaultWaitMS = 1000
const maxWaitMS = 10000

// defaultDragStepMS is the pause after each drag point when step_ms is absent.
// maxDragStepMS bounds step_ms, so a 200-point path stays under two minutes.
const (
	defaultDragStepMS = 10
	maxDragStepMS     = 500
)

// pointerDevice is the pointer the drag and pointer actions drive.
type pointerDevice interface {
	move(x, y int) error
	button(name string, pressed bool) error
}

// dragStep returns the pause after each drag point and whether the batch asked
// for it. An absent step_ms keeps the original drag timing.
func dragStep(a Action) (time.Duration, bool, error) {
	if _, ok := a["step_ms"]; !ok {
		return defaultDragStepMS * time.Millisecond, false, nil
	}
	ms, err := actionValue[int](a, "step_ms")
	if err != nil {
		return 0, false, err
	}
	if ms < 0 || ms > maxDragStepMS {
		return 0, false, fmt.Errorf("step_ms must be 0 to %d", maxDragStepMS)
	}
	return time.Duration(ms) * time.Millisecond, true, nil
}

// dragPath presses the left button at the first point, moves through the rest
// and releases. After each point it waits step. With paced, it also waits after
// the press, so a frame-driven app sees the button held before the first move.
// The button is released even when a move or a wait fails.
func dragPath(ctx context.Context, p pointerDevice, path []point, step time.Duration, paced bool, wait func(context.Context, time.Duration) error) error {
	if err := p.move(path[0].X, path[0].Y); err != nil {
		return err
	}
	if err := p.button("left", true); err != nil {
		return err
	}
	defer p.button("left", false)
	if paced {
		if err := wait(ctx, step); err != nil {
			return err
		}
	}
	for _, point := range path[1:] {
		if err := p.move(point.X, point.Y); err != nil {
			return err
		}
		if err := wait(ctx, step); err != nil {
			return err
		}
	}
	return nil
}

// pointerButtonAction runs pointer_down (move, then press) or pointer_up
// (optional move, then release). The button stays held between actions of one
// batch, so a move action in between is a real held move.
func pointerButtonAction(p pointerDevice, a Action) error {
	button := "left"
	if _, ok := a["button"]; ok {
		button, _ = actionValue[string](a, "button")
	}
	_, hasX := a["x"]
	if hasX {
		x, _ := actionValue[int](a, "x")
		y, _ := actionValue[int](a, "y")
		if err := p.move(x, y); err != nil {
			return err
		}
	}
	return p.button(button, actionType(a) == "pointer_down")
}

// EventAction copies an action for the event feed and log. A type action
// keeps its text cut to EventTextMax characters. With "secret": true it keeps
// no text and says so. Either way it records how many characters were typed.
func EventAction(a Action) Action {
	out := Action{}
	for key, value := range a {
		out[key] = value
	}
	if actionType(a) != "type" {
		return out
	}
	text, _ := actionValue[string](a, "text")
	secret, _ := actionValue[bool](a, "secret")
	out["chars"], _ = json.Marshal(utf8.RuneCountInString(text))
	if secret {
		out["text"] = json.RawMessage(`""`)
		out["secret"] = json.RawMessage(`true`)
		return out
	}
	delete(out, "secret")
	if r := []rune(text); len(r) > EventTextMax {
		text = string(r[:EventTextMax]) + "…"
	}
	out["text"], _ = json.Marshal(text)
	return out
}

func actionType(a Action) string { s, _ := actionValue[string](a, "type"); return s }
func coord(a Action, width, height int) error {
	x, err := actionValue[int](a, "x")
	if err != nil {
		return err
	}
	y, err := actionValue[int](a, "y")
	if err != nil {
		return err
	}
	if x < 0 || x >= width || y < 0 || y >= height {
		return fmt.Errorf("coordinates outside %dx%d computer", width, height)
	}
	return nil
}

// ValidateActions checks the entire batch before any input, including unknown
// fields, integer coordinates, Unicode text, and aggregate capture/wait limits.
func ValidateActions(actions []Action, width, height int) error {
	if len(actions) < 1 || len(actions) > maxActions {
		return fmt.Errorf("actions must contain 1–50 items")
	}
	waits, shots, texts := 0, 0, 0
	for i, a := range actions {
		if err := validateAction(a, width, height, &waits, &shots, &texts); err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
	}
	return nil
}
func validateAction(a Action, width, height int, waits, shots, texts *int) error {
	kind, err := actionValue[string](a, "type")
	if err != nil {
		return err
	}
	spec, ok := actionSpecs[kind]
	if !ok {
		return fmt.Errorf("unsupported action %q", kind)
	}
	allowed := " type " + strings.NewReplacer("[", "", "]", "").Replace(spec.fields) + " "
	switch kind {
	case "wait":
		if _, err := waitDuration(a); err != nil {
			return err
		}
		*waits++
		if *waits > maxWaits {
			return fmt.Errorf("at most five waits per batch")
		}
	case "screenshot":
		*shots++
		if *shots > maxScreenshots {
			return fmt.Errorf("at most ten screenshots per batch")
		}
	}
	for key := range a {
		if !strings.Contains(allowed, " "+key+" ") {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	switch kind {
	case "click", "double_click", "move", "scroll", "pointer_down":
		if err := coord(a, width, height); err != nil {
			return err
		}
	case "pointer_up":
		// Both coordinates or neither: the release can move first.
		_, hasX := a["x"]
		_, hasY := a["y"]
		if hasX != hasY {
			return fmt.Errorf("pointer_up needs both x and y, or neither")
		}
		if hasX {
			if err := coord(a, width, height); err != nil {
				return err
			}
		}
	}
	if kind == "pointer_down" || kind == "pointer_up" {
		if _, ok := a["button"]; ok {
			button, err := actionValue[string](a, "button")
			if err != nil {
				return err
			}
			if button != "left" && button != "right" && button != "middle" {
				return fmt.Errorf("button must be left, middle or right")
			}
		}
	}
	if kind == "click" {
		button, err := actionValue[string](a, "button")
		if err != nil {
			return err
		}
		if button != "left" && button != "right" && button != "middle" {
			return fmt.Errorf("button must be left, middle or right")
		}
	}
	if kind == "drag" {
		path, err := actionValue[[]point](a, "path")
		if err != nil {
			return err
		}
		if len(path) < 2 || len(path) > maxDragPoints {
			return fmt.Errorf("drag path must contain 2–200 points")
		}
		for _, p := range path {
			if p.X < 0 || p.X >= width || p.Y < 0 || p.Y >= height {
				return fmt.Errorf("drag points must be inside the %dx%d computer", width, height)
			}
		}
		if _, _, err := dragStep(a); err != nil {
			return err
		}
	}
	if kind == "scroll" {
		for _, key := range []string{"scroll_x", "scroll_y"} {
			n, err := actionValue[int](a, key)
			if err != nil {
				return err
			}
			if n < -maxScroll || n > maxScroll {
				return fmt.Errorf("%s must be -3000 to 3000", key)
			}
		}
	}
	if _, ok := a["keys"]; ok || kind == "keypress" {
		keys, err := actionValue[[]string](a, "keys")
		if err != nil {
			return err
		}
		if len(keys) < 1 || len(keys) > maxKeys {
			return fmt.Errorf("keys must contain 1–12 names")
		}
		for _, key := range keys {
			if !validKey(key) {
				return fmt.Errorf("unknown key %q", key)
			}
		}
	}
	if kind == "type" {
		if _, ok := a["secret"]; ok {
			if _, err := actionValue[bool](a, "secret"); err != nil {
				return err
			}
		}
		raw := a["text"]
		if !utf8.Valid(raw) || !validJSONUnicode(raw) {
			return fmt.Errorf("text must be valid UTF-8")
		}
		text, err := actionValue[string](a, "text")
		if err != nil {
			return err
		}
		for _, r := range text {
			if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
				return fmt.Errorf("invalid text character")
			}
		}
		*texts += utf8.RuneCountInString(text)
		if *texts > maxText {
			return fmt.Errorf("at most 10,000 text characters per batch")
		}
	}
	return nil
}

var aliases = map[string]string{"CTRL": "Control_L", "CONTROL": "Control_L", "ALT": "Alt_L", "SHIFT": "Shift_L", "META": "Super_L", "SUPER": "Super_L", "CMD": "Super_L", "ENTER": "Return", "RETURN": "Return", "ESC": "Escape", "ESCAPE": "Escape", "TAB": "Tab", "BACKSPACE": "BackSpace", "DELETE": "Delete", "SPACE": "space", "UP": "Up", "DOWN": "Down", "LEFT": "Left", "RIGHT": "Right", "HOME": "Home", "END": "End", "PAGEUP": "Page_Up", "PAGEDOWN": "Page_Down"}

func keyName(key string) string {
	if v, ok := aliases[strings.ToUpper(key)]; ok {
		return v
	}
	if utf8.RuneCountInString(key) == 1 {
		return fmt.Sprintf("U%04X", []rune(key)[0])
	}
	return key
}
func validKey(key string) bool {
	if key == "" {
		return false
	}
	if utf8.RuneCountInString(key) == 1 {
		return utf8.ValidString(key) && !unicode.IsControl([]rune(key)[0])
	}
	key = keyName(key)
	return knownKey(key)
}

// encoding/json replaces unpaired surrogate escapes. Reject those escapes
// before decoding, while allowing an explicit valid U+FFFD character.
func validJSONUnicode(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}

// actionSpecs defines supported actions, their fields, and whether they change input.
var actionSpecs = map[string]struct {
	fields string
	input  bool
}{
	"click":        {"x y button [keys]", true},
	"double_click": {"x y [keys]", true},
	"move":         {"x y", true},
	"drag":         {"path [step_ms] [keys]", true},
	"pointer_down": {"x y [button]", true},
	"pointer_up":   {"[x y] [button]", true},
	"scroll":       {"x y scroll_x scroll_y", true},
	"keypress":     {"keys", true},
	"type":         {"text [secret]", true},
	"wait":         {"[ms]", false},
	"screenshot":   {"", false},
}

// ActionHelp prints fields from the same definitions used by validation.
func ActionHelp() string {
	var out strings.Builder
	fmt.Fprintf(&out, "JSON array: 1–%d actions. Each item requires type; brackets mark optional fields.\n", maxActions)
	names := make([]string, 0, len(actionSpecs))
	for name := range actionSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fields := actionSpecs[name].fields
		if fields == "" {
			fields = "(no fields)"
		}
		fmt.Fprintf(&out, "  %s: %s", name, fields)
		if actionSpecs[name].input {
			out.WriteString(" (requires --status)")
		}
		out.WriteByte('\n')
	}
	fmt.Fprintf(&out, `
Fields:
  x, y: integer pixel coordinates inside the current screen.
  button: left, middle, or right.
  keys: 1–%d key names, such as CTRL, l, ENTER, or Escape.
  path: 2–%d points, each an object with integer x and y. A point written as
    [x, y] is accepted too.
  scroll_x, scroll_y: integers from -%d to %d; positive moves right/down.
  text: UTF-8 text, at most %d characters per batch.
  secret: true hides typed text from action events.
  ms: integer from 0 to %d; default %d milliseconds.
  step_ms: pause after each drag point, integer from 0 to %d; default %d
    milliseconds. When set, drag also pauses after the press.
  pointer_down moves to x, y and presses the button (default left). pointer_up
    optionally moves to x, y, then releases it. A move action between them is a
    held move. A button still held at the end of a batch is released.

Batch limits: %d waits and %d screenshots.
`, maxKeys, maxDragPoints, maxScroll, maxScroll, maxText, maxWaitMS, defaultWaitMS, maxDragStepMS, defaultDragStepMS, maxWaits, maxScreenshots)
	return strings.TrimSpace(out.String())
}

// InputActionNames lists the actions that require an intent, including in CLI help.
func InputActionNames() []string {
	names := []string{}
	for name, spec := range actionSpecs {
		if spec.input {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ActionStatus trims and validates the intent for a batch. Reads have no status.
func ActionStatus(actions []Action, text string) (string, error) {
	for _, action := range actions {
		if actionSpecs[actionType(action)].input {
			return RequiredStatus(text)
		}
	}
	if strings.TrimSpace(text) != "" {
		return "", fmt.Errorf("--status is only for computer actions")
	}
	return "", nil
}

// RequiredStatus checks the short intent shown to the person watching the computer.
func RequiredStatus(text string) (string, error) {
	return shortLine("--status", "say in a few words what this action is for", text)
}

// RequiredReason checks the short reason shown while a human has control.
func RequiredReason(text string) (string, error) {
	return shortLine("--reason", "say in a few words what the person should do", text)
}

func shortLine(flag, hint, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("%s is required: %s", flag, hint)
	}
	if utf8.RuneCountInString(text) > 80 {
		return "", fmt.Errorf("%s must be 80 characters or fewer", flag)
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s must be one line", flag)
		}
	}
	return text, nil
}

// waitDuration supplies the existing one-second default and bounds explicit waits.
func waitDuration(a Action) (time.Duration, error) {
	ms := defaultWaitMS
	if _, ok := a["ms"]; ok {
		var err error
		ms, err = actionValue[int](a, "ms")
		if err != nil {
			return 0, err
		}
	}
	if ms < 0 || ms > maxWaitMS {
		return 0, fmt.Errorf("ms must be 0 to %d", maxWaitMS)
	}
	return time.Duration(ms) * time.Millisecond, nil
}
