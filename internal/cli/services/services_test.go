package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeCaller answers the engine calls a test needs and records the writes it
// was asked to make.
type fakeCaller struct {
	results map[string]json.RawMessage
	writes  []write
}

type write struct {
	method string
	args   []any
}

// readMethods are the engine calls a command only reads. Every other method a
// test asks for is a write, and is recorded instead of answered.
var readMethods = map[string]bool{
	"Rooms": true, "ListServices": true, "ReviewerStatus": true, "Settings": true,
	"HookSummary": true, "LibrarianDeferred": true, "VoiceStatus": true, "Transcript": true, "TranscriptTurn": true,
}

func (f *fakeCaller) Call(_ context.Context, method string, args []any, out any) error {
	if !readMethods[method] {
		f.writes = append(f.writes, write{method: method, args: args})
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

func factsFrom(t *testing.T, r refs, reader *fakeCaller) facts {
	t.Helper()
	return readFacts(context.Background(), reader, r)
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

func TestListShowsEachServiceAndItsOwnState(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"ListServices": []map[string]any{{
			"role": "reviewer", "name": "PR reviewer", "profile": "sonnet", "pinned": "sonnet", "source": "pinned", "profiles": []string{"sonnet", "deepseek"},
		}},
		"ReviewerStatus": map[string]any{"enabled": true, "profile": "sonnet", "reviewers": []map[string]any{{"session": map[string]any{"address": "dyna:reviewer-7", "status": "running"}, "current": map[string]any{"pr": 7, "title": "Fix bounds"}}}, "recent": []map[string]any{{"pr": 6, "verdict": "approve", "at": "2026-10-23T09:00:00Z"}}},
		"Settings": []map[string]any{
			{"key": "library.enabled", "value": []string{"/repo/dyna"}},
			{"key": "library.profile", "value": "sonnet"},
		},
		"HookSummary": []map[string]any{{
			"hook":    map[string]any{"name": "librarian-main", "event": "main-updated"},
			"lastRun": map[string]any{"result": "pass", "startedAt": "2026-10-23T09:50:00Z"},
		}},
		"LibrarianLastRun":  "2026-10-23T09:50:00Z",
		"LibrarianDeferred": "2026-10-23T10:20:00Z",
		"VoiceStatus":       map[string]any{"available": true, "live": false},
	})
	rows := Rows(factsFrom(t, refs{Room: "dyna", Repo: "/repo/dyna"}, reader))
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	library, reviewer, voice := rows[0], rows[1], rows[2]

	if library.ID != Library || !library.Enabled || library.Profile != "sonnet" || library.State != "idle" || library.Session != "dyna:library" {
		t.Fatalf("librarian row = %+v", library)
	}
	if library.LastRun == "" || library.NextRun == "" {
		t.Fatalf("librarian run times = %+v", library)
	}
	if reviewer.ID != Reviewer || !reviewer.Enabled || reviewer.State != "running" || reviewer.Session != "dyna:reviewer-7" || !strings.Contains(reviewer.Detail, "#7") {
		t.Fatalf("reviewer row = %+v", reviewer)
	}
	if reviewer.LastRun == "" {
		t.Fatalf("reviewer last run missing: %+v", reviewer)
	}
	if voice.ID != Voice || !voice.Enabled || voice.State != "idle" || voice.Switch != "" {
		t.Fatalf("voice row = %+v", voice)
	}
}

func TestLibrarianShowsNoNextRunWhenNothingIsDeferred(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"Settings":          []map[string]any{{"key": "library.enabled", "value": []string{"/repo/dyna"}}},
		"HookSummary":       []map[string]any{{"hook": map[string]any{"name": "librarian-main", "event": "main-updated"}}},
		"LibrarianDeferred": nil,
	})
	library := Rows(factsFrom(t, refs{Room: "dyna", Repo: "/repo/dyna"}, reader))[0]
	if library.NextRun != "" {
		t.Fatalf("librarian next run = %q, want none", library.NextRun)
	}
}

func TestLibrarianOffOutsideItsRepositories(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"ListServices":   []map[string]any{},
		"ReviewerStatus": map[string]any{"enabled": false},
		"Settings":       []map[string]any{{"key": "library.enabled", "value": []string{"/repo/other"}}},
		"HookSummary":    []map[string]any{},
		"VoiceStatus":    map[string]any{"available": false, "unavailable": "no account"},
	})
	rows := Rows(factsFrom(t, refs{Room: "dyna", Repo: "/repo/dyna"}, reader))
	if rows[0].Enabled || rows[0].State != "off" {
		t.Fatalf("librarian row = %+v", rows[0])
	}
	if rows[2].Enabled || rows[2].State != "off" || rows[2].Detail != "no account" {
		t.Fatalf("voice row = %+v", rows[2])
	}
}

// Library and reviewer profile writes use the same engine call.
func TestProfileWritesTheOwningCall(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"ListServices":   []map[string]any{{"role": "reviewer", "profiles": []string{"sonnet", "deepseek"}}},
		"ReviewerStatus": map[string]any{"enabled": true},
		"Settings":       []map[string]any{},
		"HookSummary":    []map[string]any{},
		"VoiceStatus":    map[string]any{"available": true},
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
		"ListServices":   []map[string]any{{"role": "reviewer", "profiles": []string{"sonnet"}}},
		"ReviewerStatus": map[string]any{"enabled": true},
		"Settings":       []map[string]any{},
		"HookSummary":    []map[string]any{},
		"VoiceStatus":    map[string]any{"available": true},
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

// A service name is one of the known set, and an alias names the same service.
// A room given as an ID, or found through the repository, resolves to the name
// a session address needs.
func TestRoomResolvesToItsName(t *testing.T) {
	reader := readerWith(t, map[string]any{
		"ListServices":   []map[string]any{},
		"ReviewerStatus": map[string]any{"enabled": true},
		"Settings":       []map[string]any{{"key": "library.enabled", "value": []string{"/repo/dyna"}}},
		"HookSummary":    []map[string]any{},
		"VoiceStatus":    map[string]any{"available": true},
	})
	byID := factsFrom(t, refs{Room: "room_1", Repo: "/repo/dyna"}, reader)
	if byID.refs.Room != "dyna" {
		t.Fatalf("room from ID = %q; want dyna", byID.refs.Room)
	}
	byRepo := factsFrom(t, refs{Repo: "/repo/dyna"}, reader)
	if byRepo.refs.Room != "dyna" {
		t.Fatalf("room from repository = %q; want dyna", byRepo.refs.Room)
	}
	if rows := Rows(byRepo); rows[0].Session != "dyna:library" {
		t.Fatalf("librarian session = %q; want dyna:library", rows[0].Session)
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
	for text, want := range map[string]ServiceID{"librarian": Library, "reviewers": Reviewer, "voice": Voice} {
		got, err := ParseServiceID(text)
		if err != nil || got != want {
			t.Fatalf("%s = %q, %v; want %q", text, got, err, want)
		}
	}
	if _, err := ParseServiceID("merge"); err == nil {
		t.Fatal("unknown service accepted")
	}
}
