package computers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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

// The guide and the action schema show points as objects, and the array form a
// reader may copy from an older example must run too.
func TestDragPathAcceptsPointArrays(t *testing.T) {
	actions := parseActions(t, `[{"type":"drag","path":[[80,46],[900,46]]},{"type":"drag","path":[{"x":10,"y":20},{"x":30,"y":40}]}]`)
	if err := ValidateActions(actions, 1280, 800); err != nil {
		t.Fatalf("a point array was refused: %v", err)
	}
	path, err := actionValue[[]point](actions[0], "path")
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].X != 80 || path[0].Y != 46 || path[1].X != 900 || path[1].Y != 46 {
		t.Fatalf("array points decoded as %#v", path)
	}
	named, err := actionValue[[]point](actions[1], "path")
	if err != nil {
		t.Fatal(err)
	}
	if named[0].X != 10 || named[0].Y != 20 || named[1].X != 30 {
		t.Fatalf("object points decoded as %#v", named)
	}
	for _, bad := range []string{
		`[{"type":"drag","path":[[80],[900,46]]}]`,
		`[{"type":"drag","path":[{"x":80},{"x":80,"y":46}]}]`,
		// A null coordinate must not decode as 0 and start a real drag.
		`[{"type":"drag","path":[[null,46],[900,46]]}]`,
		`[{"type":"drag","path":[[80,null],[900,46]]}]`,
		`[{"type":"drag","path":[{"x":null,"y":46},{"x":900,"y":46}]}]`,
	} {
		if err := ValidateActions(parseActions(t, bad), 1280, 800); err == nil {
			t.Fatalf("an incomplete point was accepted: %s", bad)
		}
	}
}

func TestPointerActionValidation(t *testing.T) {
	good := `[{"type":"pointer_down","x":5,"y":6},{"type":"move","x":9,"y":6},{"type":"pointer_up"},{"type":"pointer_up","x":9,"y":6,"button":"right"},{"type":"drag","step_ms":120,"path":[{"x":0,"y":0},{"x":1,"y":1}]}]`
	if err := ValidateActions(parseActions(t, good), 1280, 800); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`[{"type":"pointer_down"}]`,
		`[{"type":"pointer_down","x":1280,"y":0}]`,
		`[{"type":"pointer_down","x":1,"y":1,"button":"fourth"}]`,
		`[{"type":"pointer_up","x":1}]`,
		`[{"type":"drag","step_ms":501,"path":[{"x":0,"y":0},{"x":1,"y":1}]}]`,
		`[{"type":"drag","step_ms":-1,"path":[{"x":0,"y":0},{"x":1,"y":1}]}]`,
		`[{"type":"drag","step_ms":"slow","path":[{"x":0,"y":0},{"x":1,"y":1}]}]`,
	} {
		if err := ValidateActions(parseActions(t, bad), 1280, 800); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// recorder logs pointer events and waits in order.
type recorder struct{ log []string }

func (r *recorder) move(x, y int) error {
	r.log = append(r.log, fmt.Sprintf("move %d,%d", x, y))
	return nil
}
func (r *recorder) button(name string, pressed bool) error {
	state := "up"
	if pressed {
		state = "down"
	}
	r.log = append(r.log, name+" "+state)
	return nil
}
func (r *recorder) scroll(x, y int) error {
	r.log = append(r.log, fmt.Sprintf("scroll %d,%d", x, y))
	return nil
}
func (r *recorder) wait(_ context.Context, d time.Duration) error {
	r.log = append(r.log, "wait "+d.String())
	return nil
}

func TestTakeoverDuringAHeldWaitReleasesAtOnce(t *testing.T) {
	r := &recorder{}
	g := newGuardedPointer(r)
	downCtx, cancelDown := context.WithCancel(context.Background())
	g.begin(cancelDown)
	for _, a := range parseActions(t, `[{"type":"pointer_down","x":5,"y":6}]`) {
		if err := pointerButtonAction(g, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.end(nil); err != nil {
		t.Fatal(err)
	}
	// The next action is a long wait. The takeover lands during it.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	g.begin(cancelWait)
	g.interrupt()
	if waitCtx.Err() == nil {
		t.Fatal("the held wait was not cancelled by the takeover")
	}
	if got := strings.Join(r.log, "|"); got != "move 5,6|left down|left up" {
		t.Fatalf("events = %s, want the button released at the takeover", got)
	}
	if err := g.end(waitCtx.Err()); !errors.Is(err, errControlLost) {
		t.Fatalf("wait error = %v, want the control-lost error", err)
	}
	_ = downCtx
}

func TestTakeoverAtTheHandoffBoundaryPressesNothing(t *testing.T) {
	r := &recorder{}
	g := newGuardedPointer(r)
	_, cancel := context.WithCancel(context.Background())
	g.begin(cancel)
	// A person takes control after the action began and before its press.
	g.interrupt()
	err := pointerButtonAction(g, parseActions(t, `[{"type":"pointer_down","x":5,"y":6}]`)[0])
	if !errors.Is(err, errControlLost) || len(r.log) != 0 {
		t.Fatalf("err = %v, events = %v, want no input after the takeover", err, r.log)
	}
}

func TestDragStopsAndReleasesAtTheTakeover(t *testing.T) {
	r := &recorder{}
	g := newGuardedPointer(r)
	_, cancel := context.WithCancel(context.Background())
	g.begin(cancel)
	waits := 0
	wait := func(ctx context.Context, d time.Duration) error {
		waits++
		if waits == 2 {
			g.interrupt()
		}
		return r.wait(ctx, d)
	}
	err := g.end(dragPath(context.Background(), g, []point{{1, 1}, {2, 1}, {3, 1}, {4, 1}, {5, 1}}, time.Second, true, wait))
	if !errors.Is(err, errControlLost) {
		t.Fatalf("err = %v, want the control-lost error", err)
	}
	want := "move 1,1|left down|wait 1s|move 2,1|left up|wait 1s"
	if got := strings.Join(r.log, "|"); got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
}

func TestDragPathEventSequence(t *testing.T) {
	path := []point{{1, 1}, {2, 1}, {3, 1}}
	plain := &recorder{}
	if err := dragPath(context.Background(), plain, path, 10*time.Millisecond, false, plain.wait); err != nil {
		t.Fatal(err)
	}
	want := "move 1,1|left down|move 2,1|wait 10ms|move 3,1|wait 10ms|left up"
	if got := strings.Join(plain.log, "|"); got != want {
		t.Fatalf("default drag = %s, want %s", got, want)
	}
	paced := &recorder{}
	if err := dragPath(context.Background(), paced, path, 120*time.Millisecond, true, paced.wait); err != nil {
		t.Fatal(err)
	}
	want = "move 1,1|left down|wait 120ms|move 2,1|wait 120ms|move 3,1|wait 120ms|left up"
	if got := strings.Join(paced.log, "|"); got != want {
		t.Fatalf("paced drag = %s, want %s", got, want)
	}
}

func TestPointerDownAndUpAreSeparateEvents(t *testing.T) {
	r := &recorder{}
	for _, a := range parseActions(t, `[{"type":"pointer_down","x":5,"y":6},{"type":"pointer_up","x":9,"y":6,"button":"right"},{"type":"pointer_up"}]`) {
		if err := pointerButtonAction(r, a); err != nil {
			t.Fatal(err)
		}
	}
	want := "move 5,6|left down|move 9,6|right up|left up"
	if got := strings.Join(r.log, "|"); got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
}
