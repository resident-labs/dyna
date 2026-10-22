package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/guidance"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
)

// The internal root runs in root mode with its room stamp. Its session remains
// the parent of non-service work that it starts.
func TestRootModeSessionParentsChildInItsRoom(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	rec, err := e.m.rooms().Create(room.CreateInput{Name: "seat-room"})
	if err != nil {
		t.Fatal(err)
	}
	const rootID = "ses_20260101-120000-abcdef12"
	if err := e.m.Store.writeMeta(Meta{ID: rootID, Name: RoleRoot, Role: RoleRoot, RoomID: rec.ID, Room: rec.Name, Depth: 1, Service: true, Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	rootModeEnv(t, rec.ID)
	t.Setenv(mode.EnvRoom, rec.Name)
	info, _ := e.start("RESPOND: done")
	if info.RoomID != rec.ID || info.ParentSession != rootID || info.Depth != 2 || info.Service {
		t.Fatalf("started %+v, want a non-service child of root in %s", info.Meta, rec.ID)
	}
	e.wait(info.ID, 1)
}

// The internal root's worker runs in root mode: every Dyna command treats it
// as the room owner, and DYNA_SESSION only attributes its work.
func TestStampSessionRunsTheInternalRootInRootMode(t *testing.T) {
	env := map[string]string{mode.EnvDepth: "2", mode.EnvAddress: "work:parent"}
	stampSession(env, Meta{ID: "ses_root", Name: RoleRoot, Role: RoleRoot, RoomID: "room_x", Room: "work", Depth: 1})
	want := map[string]string{mode.EnvMode: mode.Root, mode.EnvSession: "ses_root", mode.EnvDepth: "", mode.EnvRoomID: "room_x", mode.EnvRoom: "work", mode.EnvAddress: ""}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if _, ok := mode.CurrentSession(); ok || !mode.OwnerProcess() {
		t.Fatal("the internal root's worker is not the owner")
	}
	stampSession(env, Meta{ID: "ses_a", Name: "a", RoomID: "room_x", Room: "work", Depth: 1})
	if env[mode.EnvMode] != mode.Subagent || env[mode.EnvAddress] != "work:a" || env[mode.EnvDepth] != "1" {
		t.Fatalf("subagent stamp = %v", env)
	}
}

// The internal root's prompts carry the root block that `dyna guide` shows the
// seat holder, not the subagent contract.
func TestInternalRootPromptCarriesTheRootBlock(t *testing.T) {
	meta := Meta{ID: "ses_root", Name: RoleRoot, Role: RoleRoot, RoomID: "room_x", Room: "work", Depth: 1}
	got := rulesFor(meta) + "\n\n" + workerContext(meta, "", 3)
	for _, want := range []string{"[DYNA ROOT]", guidance.RootBlock(true), "- `delegation`: ", "You are the internal root of room work"} {
		if !strings.Contains(got, want) {
			t.Errorf("root instructions miss %q", want)
		}
	}
	if strings.Contains(got, "[DYNA SESSION]") || strings.Contains(got, "`"+guidance.RootTopic+"`") {
		t.Error("root instructions carry the subagent contract or the external-orchestration topic")
	}
	if rulesFor(meta) == rulesFor(Meta{}) {
		t.Error("root and subagent share one contract")
	}
}

// Every post that makes the internal root answer the human owes a report: the
// turn-start drain and live steering both ask Owes. Agent reports and the
// root's own owner posts owe nothing, so those turns end silently.
func TestInternalRootOwesTheHumanAnAnswer(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	root := Meta{ID: "ses_root", Name: RoleRoot, Role: RoleRoot, Room: "work"}
	for _, tc := range []struct {
		name string
		msg  room.Message
		want bool
	}{
		{"human post to @root", room.Message{From: "work", Human: true, To: []string{"work"}, Via: []string{"@root"}}, true},
		{"human post with no mention", room.Message{From: "work", Human: true, To: []string{"work"}}, true},
		{"handoff", room.Message{From: "work", To: []string{"work:root"}, Via: []string{"@root"}}, true},
		{"agent post to @root", room.Message{From: "work:a", To: []string{"work"}, Via: []string{"@root"}}, false},
		{"agent report", room.Message{From: "work:a", Kind: "completed", To: []string{"work"}, OwnerReport: true}, false},
		{"root's own owner post", room.Message{From: "work", To: []string{"work"}, Via: []string{"@root"}}, false},
	} {
		if got := s.Owes(root, tc.msg); got != tc.want {
			t.Errorf("%s: Owes = %v, want %v", tc.name, got, tc.want)
		}
	}
}
