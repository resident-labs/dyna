package computers

import (
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
	case "click", "double_click", "move", "scroll":
		if err := coord(a, width, height); err != nil {
			return err
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
		path, err := actionValue[[]Action](a, "path")
		if err != nil {
			return err
		}
		if len(path) < 2 || len(path) > maxDragPoints {
			return fmt.Errorf("drag path must contain 2–200 points")
		}
		for _, p := range path {
			if len(p) != 2 {
				return fmt.Errorf("drag points contain exactly x and y")
			}
			if err := coord(p, width, height); err != nil {
				return err
			}
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
	"drag":         {"path [keys]", true},
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
  path: 2–%d points, each with integer x and y.
  scroll_x, scroll_y: integers from -%d to %d; positive moves right/down.
  text: UTF-8 text, at most %d characters per batch.
  secret: true hides typed text from action events.
  ms: integer from 0 to %d; default %d milliseconds.

Batch limits: %d waits and %d screenshots.
`, maxKeys, maxDragPoints, maxScroll, maxScroll, maxText, maxWaitMS, defaultWaitMS, maxWaits, maxScreenshots)
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
