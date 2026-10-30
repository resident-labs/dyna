package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/serviceregistry"
)

// fakeCaller answers the engine calls a test needs and records the writes it
// was asked to make.
type fakeCaller struct {
	results map[string]json.RawMessage
	writes  []write
	// registryArgs are the arguments of the last ServiceRegistry read.
	registryArgs []any
}

type write struct {
	method string
	args   []any
}

// readMethods are the engine calls a command only reads. Every other method a
// test asks for is a write, and is recorded instead of answered.
var readMethods = map[string]bool{
	"Rooms": true, "ListServices": true, "ServiceRegistry": true, "Transcript": true, "TranscriptTurn": true,
}

func (f *fakeCaller) Call(_ context.Context, method string, args []any, out any) error {
	if !readMethods[method] {
		f.writes = append(f.writes, write{method: method, args: args})
	}
	if method == "ServiceRegistry" {
		f.registryArgs = args
	}
	raw, ok := f.results[method]
	if !ok {
		if readMethods[method] {
			return fmt.Errorf("unexpected read %s", method)
		}
		return nil
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (f *fakeCaller) write(method string) (write, bool) {
	for _, w := range f.writes {
		if w.method == method {
			return w, true
		}
	}
	return write{}, false
}

func readerWith(t *testing.T, results map[string]any) *fakeCaller {
	t.Helper()
	if _, ok := results["Rooms"]; !ok {
		results["Rooms"] = []map[string]any{{"id": "room_1", "name": "dyna", "repo": "/repo/dyna"}}
	}
	f := &fakeCaller{results: map[string]json.RawMessage{}}
	for method, value := range results {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		f.results[method] = raw
	}
	return f
}

// Library and reviewer profile writes use the same engine call.
func TestProfileWritesTheOwningCall(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"ListServices": []map[string]any{{"role": "reviewer", "profiles": []string{"sonnet", "deepseek"}}},
	})
	var out bytes.Buffer
	if err := setProfile(context.Background(), reader, refs{Room: "dyna", Repo: "/repo/dyna"}, Reviewer, "deepseek", &out); err != nil {
		t.Fatal(err)
	}
	w, ok := reader.write("SetServiceProfile")
	if !ok || fmt.Sprint(w.args) != "[dyna reviewer deepseek]" {
		t.Fatalf("reviewer profile write = %+v", reader.writes)
	}
	if err := setProfile(context.Background(), reader, refs{Room: "dyna", Repo: "/repo/dyna"}, Library, "sonnet", &out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range reader.writes {
		if w.method == "SetServiceProfile" && fmt.Sprint(w.args) == "[dyna library sonnet]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("librarian profile write = %+v", reader.writes)
	}
}

// A profile that cannot run a service is refused before any write, and voice
// has no profile of its own.
func TestProfileRefusesUnusableChoice(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"ListServices": []map[string]any{{"role": "reviewer", "profiles": []string{"sonnet"}}},
	})
	var out bytes.Buffer
	err := setProfile(context.Background(), reader, refs{Room: "dyna"}, Reviewer, "grok", &out)
	if err == nil || !strings.Contains(err.Error(), "sonnet") {
		t.Fatalf("unusable profile: %v", err)
	}
	if len(reader.writes) != 0 {
		t.Fatalf("refused profile still wrote: %+v", reader.writes)
	}
	if err := setProfile(context.Background(), reader, refs{Room: "dyna"}, Voice, "", &out); err == nil {
		t.Fatal("voice accepted a profile")
	}
}

// Voice has no switch: enable and disable refuse instead of pretending.
func TestEnableRefusesVoice(t *testing.T) {
	reader := readerWith(t, map[string]any{})
	var out bytes.Buffer
	err := setEnabled(context.Background(), reader, refs{Room: "dyna"}, Voice, true, &out)
	if err == nil || !strings.Contains(err.Error(), "no switch") {
		t.Fatalf("voice switch: %v", err)
	}
	if len(reader.writes) != 0 {
		t.Fatalf("voice switch wrote: %+v", reader.writes)
	}
}

// A write resolves the room and its repository exactly as a read does: a
// caller standing in a worktree switches the librarian of the room's own
// repository, and a room named by its ID is found. An explicit --repo wins.
func TestWritesUseTheRoomRepository(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"Rooms":           []map[string]any{{"id": "room_1", "name": "dyna", "repo": "/repo/main"}},
		"EnableLibrarian": map[string]any{"repo": map[string]any{"main": "/repo/main"}, "room": "dyna"},
	})
	var out bytes.Buffer
	if err := setEnabled(context.Background(), reader, refs{Room: "room_1", Repo: "/worktrees/w1"}, Library, true, &out); err != nil {
		t.Fatal(err)
	}
	w, ok := reader.write("EnableLibrarian")
	if !ok || len(w.args) != 3 {
		t.Fatalf("librarian enable write = %+v", reader.writes)
	}
	if w.args[0] != "/repo/main" || w.args[1] != "dyna" {
		t.Fatalf("librarian enable args = %v; want the room's repository and name", w.args)
	}

	reader.writes = nil
	if err := setEnabled(context.Background(), reader, refs{Room: "dyna", Repo: "/repo/mine", RepoExplicit: true}, Library, false, &out); err != nil {
		t.Fatal(err)
	}
	w, ok = reader.write("DisableLibrarian")
	if !ok || fmt.Sprint(w.args) != "[/repo/mine]" {
		t.Fatalf("explicit --repo write = %+v", reader.writes)
	}
}

func TestParseServiceIDAcceptsAliases(t *testing.T) {
	for text, want := range map[string]ServiceID{"librarian": Library, "reviewers": Reviewer, "voice": Voice, "merge-queue": serviceregistry.IDMergeQueue} {
		got, err := ParseServiceID(text)
		if err != nil || got != want {
			t.Fatalf("%s = %q, %v; want %q", text, got, err, want)
		}
	}
	if _, err := ParseServiceID("nonsense"); err == nil {
		t.Fatal("unknown service accepted")
	}
}

// The list and the engine call the Services page reads are one source: the
// JSON the list prints is the call's answer, and the text lists every service
// under its group.
func TestListMatchesTheRegistryCall(t *testing.T) {
	at := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	hook := serviceregistry.HookFacts{Found: true, Runs: []serviceregistry.HookRun{{Result: "pass", StartedAt: at}}}
	registry := serviceregistry.Build(serviceregistry.Facts{
		Reviewer: &serviceregistry.ReviewerFacts{Enabled: true, Profile: "sonnet", Queued: 2},
		Hooks:    map[serviceregistry.ID]serviceregistry.HookFacts{serviceregistry.IDMaintenance: hook},
	})
	reader := readerWith(t, map[string]any{"ServiceRegistry": registry})
	rows, err := readRegistry(context.Background(), reader, refs{Room: "dyna"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(rows)
	want, _ := json.Marshal(registry)
	if !bytes.Equal(got, want) {
		t.Fatalf("list rows differ from the call:\n%s\n%s", got, want)
	}
	var out bytes.Buffer
	printRows(&out, rows)
	text := out.String()
	for _, part := range []string{"Pull requests", "Agents", "This computer", "reviewer", "2 waiting", "maintenance", "not-set-up"} {
		if !strings.Contains(text, part) {
			t.Errorf("list lacks %q:\n%s", part, text)
		}
	}
	for _, id := range serviceregistry.IDs() {
		if !strings.Contains(text, string(id)) {
			t.Errorf("list lacks service %s", id)
		}
	}
}

// A built-in service switches through its global hook, and a service that has
// no switch refuses.
func TestEnableSwitchesBuiltinHook(t *testing.T) {
	reader := readerWith(t, map[string]any{})
	var out bytes.Buffer
	if err := setEnabled(context.Background(), reader, refs{Room: "dyna"}, serviceregistry.IDMaintenance, false, &out); err != nil {
		t.Fatal(err)
	}
	w, ok := reader.write("EditService")
	if !ok || w.args[0] != "maintenance" {
		t.Fatalf("switch write = %+v", reader.writes)
	}
	if err := setEnabled(context.Background(), reader, refs{Room: "dyna"}, serviceregistry.IDLocalCI, false, &out); err == nil {
		t.Fatal("a service without a switch accepted one")
	}
}

// An explicit --repo reaches the registry call, so a list shows the repository
// that a switch will change.
func TestListReadsTheChosenRepository(t *testing.T) {
	reader := readerWith(t, map[string]any{"ServiceRegistry": []serviceregistry.Service{}})
	if _, err := readRegistry(context.Background(), reader, refs{Room: "dyna", Repo: "/other", RepoExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(reader.registryArgs) != "[dyna /other]" {
		t.Fatalf("registry args = %v", reader.registryArgs)
	}
}

// set writes one typed edit with whole minutes and a duration, and refuses a
// service that has no timer, a fractional interval and an empty change.
func TestSetWritesTheScheduleEdit(t *testing.T) {
	reader := readerWith(t, map[string]any{})
	var out bytes.Buffer
	if err := setSchedule(context.Background(), reader, serviceregistry.IDMaintenance, 30*time.Minute, 90*time.Second, true, true, &out); err != nil {
		t.Fatal(err)
	}
	w, ok := reader.write("EditService")
	if !ok || len(w.args) != 2 || w.args[0] != "maintenance" {
		t.Fatalf("writes = %+v", reader.writes)
	}
	patch, _ := w.args[1].(hooks.ServicePatch)
	if patch.EveryMinutes == nil || *patch.EveryMinutes != 30 || patch.Timeout == nil || *patch.Timeout != "1m30s" || patch.Disabled != nil {
		t.Fatalf("patch = %+v", patch)
	}
	for _, c := range []struct {
		id                      ServiceID
		interval                time.Duration
		setInterval, setTimeout bool
	}{
		{serviceregistry.IDReviews, time.Minute, true, false},
		{serviceregistry.IDMaintenance, 90 * time.Second, true, false},
		{serviceregistry.IDMaintenance, 0, false, false},
	} {
		if err := setSchedule(context.Background(), reader, c.id, c.interval, 0, c.setInterval, c.setTimeout, &out); err == nil {
			t.Errorf("set %s accepted %+v", c.id, c)
		}
	}
	if len(reader.writes) != 1 {
		t.Fatalf("a refused change wrote: %+v", reader.writes)
	}
}
