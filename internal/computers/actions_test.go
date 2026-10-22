package computers

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseActions(t *testing.T, s string) []Action {
	t.Helper()
	var a []Action
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestActionValidation(t *testing.T) {
	valid := `[{"type":"click","x":0,"y":799,"button":"left","keys":["SHIFT"]},{"type":"drag","path":[{"x":0,"y":0},{"x":1279,"y":799}]},{"type":"scroll","x":2,"y":3,"scroll_x":-3000,"scroll_y":3000},{"type":"keypress","keys":["CTRL","a"]},{"type":"type","text":"Hello λ 世界\r\n"},{"type":"wait"},{"type":"screenshot"}]`
	if err := ValidateActions(parseActions(t, valid), 1280, 800); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[]`, `[{"type":"click","x":true,"y":0,"button":"left"}]`, `[{"type":"move","x":1.0,"y":1}]`, `[{"type":"move","x":1280,"y":1}]`, `[{"type":"click","x":0,"y":1,"button":"fourth"}]`, `[{"type":"move","x":0,"y":0,"extra":1}]`, `[{"type":"scroll","x":0,"y":0,"scroll_x":0,"scroll_y":3001}]`, `[{"type":"drag","path":[{"x":0,"y":0,"extra":0},{"x":1,"y":1}]}]`, `[{"type":"keypress","keys":["not_a_keysym"]}]`, `[{"type":"type","text":"\u0000"}]`, `[{"type":"type","text":"\ud800"}]`, `[{"type":"wait","seconds":3}]`} {
		if err := ValidateActions(parseActions(t, bad), 1280, 800); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
func TestActionBatchLimits(t *testing.T) {
	for _, tc := range []struct {
		kind string
		n    int
	}{{"wait", 6}, {"screenshot", 11}, {"move", 51}} {
		a := []Action{}
		for range tc.n {
			if tc.kind == "move" {
				a = append(a, parseActions(t, `[{"type":"move","x":0,"y":0}]`)[0])
			} else {
				a = append(a, parseActions(t, `[{"type":"`+tc.kind+`"}]`)[0])
			}
		}
		if ValidateActions(a, 1280, 800) == nil {
			t.Fatal(tc)
		}
	}
	text := make([]byte, 10001)
	for i := range text {
		text[i] = 'a'
	}
	b, _ := json.Marshal(string(text))
	if ValidateActions([]Action{{"type": json.RawMessage(`"type"`), "text": b}}, 1280, 800) == nil {
		t.Fatal("accepted too much text")
	}
}

func TestTypeSecretField(t *testing.T) {
	if err := ValidateActions(parseActions(t, `[{"type":"type","text":"x","secret":true}]`), 1280, 800); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[{"type":"type","text":"x","secret":"yes"}]`, `[{"type":"click","x":0,"y":0,"button":"left","secret":true}]`} {
		if err := ValidateActions(parseActions(t, bad), 1280, 800); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestEventActionKeepsTypedTextUnlessSecret(t *testing.T) {
	text := func(a Action) string { s, _ := actionValue[string](a, "text"); return s }
	got := EventAction(parseActions(t, `[{"type":"type","text":"SAVE10"}]`)[0])
	if text(got) != "SAVE10" || string(got["chars"]) != "6" || got["secret"] != nil {
		t.Errorf("short text changed: %v", got)
	}
	long := EventAction(parseActions(t, `[{"type":"type","text":"`+strings.Repeat("é", 40)+`"}]`)[0])
	if text(long) != strings.Repeat("é", EventTextMax)+"…" || string(long["chars"]) != "40" {
		t.Errorf("long text not cut: %v", long)
	}
	secret := EventAction(parseActions(t, `[{"type":"type","text":"hunter2","secret":true}]`)[0])
	if text(secret) != "" || string(secret["secret"]) != "true" || string(secret["chars"]) != "7" {
		t.Errorf("secret text leaked: %v", secret)
	}
	if strings.Contains(string(mustJSON(t, secret)), "hunter2") {
		t.Error("secret text in event")
	}
	click := parseActions(t, `[{"type":"click","x":1,"y":2,"button":"left"}]`)[0]
	if out := EventAction(click); len(out) != len(click) {
		t.Errorf("click changed: %v", out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWaitAcceptsBoundedMilliseconds(t *testing.T) {
	for _, ms := range []string{"0", "2500", "10000"} {
		if err := ValidateActions(parseActions(t, `[{"type":"wait","ms":`+ms+`}]`), 1280, 800); err != nil {
			t.Errorf("ms=%s: %v", ms, err)
		}
	}
	for _, ms := range []string{"-1", "10001", "0.5", `"2500"`, "null"} {
		if err := ValidateActions(parseActions(t, `[{"type":"wait","ms":`+ms+`}]`), 1280, 800); err == nil {
			t.Errorf("accepted ms=%s", ms)
		}
	}
}
