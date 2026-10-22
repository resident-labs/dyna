package computers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActionStatusValidation(t *testing.T) {
	for _, kind := range InputActionNames() {
		actions := []Action{{"type": json.RawMessage(`"` + kind + `"`)}}
		for _, text := range []string{"", " \t "} {
			if _, err := ActionStatus(actions, text); err == nil || err.Error() != "--status is required: say in a few words what this action is for" {
				t.Fatalf("%s: %v", kind, err)
			}
		}
		if got, err := ActionStatus(actions, "  Checking the page  "); err != nil || got != "Checking the page" {
			t.Fatalf("%s: %q %v", kind, got, err)
		}
	}
	for _, text := range []string{strings.Repeat("x", 81), "Checking\nthe page"} {
		if _, err := RequiredStatus(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if _, err := RequiredStatus(strings.Repeat("界", 80)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"screenshot", "wait"} {
		if _, err := ActionStatus([]Action{{"type": json.RawMessage(`"` + kind + `"`)}}, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActionStatusSavedAndReadDoesNotReplaceIt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "status-test"
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{meta: Meta{SessionID: id}, state: Status{SessionID: id, Width: 1280, Height: 800}, inputGate: make(chan struct{}, 1)}
	var req Request
	if err := json.Unmarshal([]byte(`{"op":"act","status":"  Checking the search field  ","actions":[{"type":"click","x":1,"y":1,"button":"left"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.act(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(Dir(id), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		LatestStatus struct {
			Text string
			At   string
		} `json:"latest_status"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.LatestStatus.Text != "Checking the search field" || saved.LatestStatus.At == "" {
		t.Fatalf("saved status: %s", data)
	}
	req = Request{Op: "act", Actions: parseActions(t, `[{"type":"screenshot"}]`)}
	if _, err := s.act(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(Dir(id), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(after) {
		t.Fatalf("read changed latest status: %s", after)
	}
}
