package issue

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestIssueCommands(t *testing.T) {
	f := newIssueFixture(t)
	r, svc := f.rec, f.svc
	run := func(args ...string) (string, error) {
		c := Command(Options{Service: func() *roomsvc.Service { return svc }})
		b := &bytes.Buffer{}
		c.SetOut(b)
		c.SetErr(b)
		c.SetArgs(args)
		err := c.ExecuteContext(context.Background())
		return b.String(), err
	}
	if _, err := run("--room", r.Name, "Bad", "--at", "file.go:zero"); err == nil {
		t.Fatal("bad location accepted")
	}
	if _, err := run("--room", r.Name, "Fix one", "--at", "file.go:12", "--note", "context"); err != nil {
		t.Fatal(err)
	}
	raw, err := run("--room", r.Name, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []room.Issue
	if err = json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 || rows[0].File != "file.go" || rows[0].Line != 12 || rows[0].Filer != "work" {
		t.Fatalf("list: %s %v", raw, err)
	}
	id := rows[0].ID
	if _, err = run("--room", r.Name, "close", id); err == nil {
		t.Fatal("missing reason accepted")
	}
	if _, err = run("--room", r.Name, "close", id, "--reason", "fixed elsewhere"); err != nil {
		t.Fatal(err)
	}
	raw, err = run("--room", r.Name, "list", "--json")
	if err != nil || raw != "[]\n" {
		t.Fatal(raw, err)
	}
	raw, err = run("--room", r.Name, "list", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 || rows[0].State != "closed" {
		t.Fatal(raw, err)
	}
}

func TestIssueStampedCaller(t *testing.T) {
	f := newIssueFixture(t)
	rooms, r, svc := f.rooms, f.rec, f.svc
	other, err := rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := svc.Notify.Sessions.Store
	meta := subagent.Meta{ID: "ses_20261019-000001-aaaaaaaa", Name: "agent", RoomID: r.ID, Room: r.Name, Depth: 1, Status: subagent.StatusIdle, Resumable: true, HarnessSession: "fixture"}
	recordtest.Write(t, sessions.Root, meta.ID, meta)
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, meta.ID)
	t.Setenv(mode.EnvRoom, r.Name)
	t.Setenv(mode.EnvRoomID, r.ID)
	t.Setenv(mode.EnvDepth, "1")
	run := func(args ...string) (string, error) {
		c := Command(Options{Service: func() *roomsvc.Service { return svc }})
		b := &bytes.Buffer{}
		c.SetOut(b)
		c.SetErr(b)
		c.SetArgs(args)
		err := c.Execute()
		return b.String(), err
	}
	raw, err := run("Stamped issue", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var i room.Issue
	if err = json.Unmarshal([]byte(raw), &i); err != nil || i.Filer != "work:agent" {
		t.Fatal(raw, err)
	}
	if _, err = run("close", i.ID, "--reason", "done"); err == nil {
		t.Fatal("agent closed issue")
	}
	// D-111: a filed issue wakes nobody, so an agent may file one in another room,
	// and its filer names the room it came from. Triage stays refused below.
	raw, err = run("--room", other.ID, "Other issue", "--json")
	if err != nil {
		t.Fatalf("agent could not file an issue in another room: %v", err)
	}
	var foreign room.Issue
	if err = json.Unmarshal([]byte(raw), &foreign); err != nil || foreign.Filer != "work:agent" || foreign.Title != "Other issue" {
		t.Fatalf("issue filed in another room: %s %v", raw, err)
	}
	if _, err = run("--room", other.ID, "close", foreign.ID, "--reason", "done"); err == nil {
		t.Fatal("agent triaged another room's issue")
	}
	if _, err = sessions.UpdateMeta(meta.ID, func(m *subagent.Meta) { m.Name = "root" }); err != nil {
		t.Fatal(err)
	}
	if _, err = run("close", i.ID, "--reason", "done"); err == nil {
		t.Fatal("a session named root closed an issue without the root role")
	}
	// The room's root seat is the stored role, not the name an address shows.
	if _, err = sessions.UpdateMeta(meta.ID, func(m *subagent.Meta) { m.Role = subagent.RoleRoot }); err != nil {
		t.Fatal(err)
	}
	if _, err = run("close", i.ID, "--reason", "done"); err != nil {
		t.Fatal(err)
	}
}

func TestIssueFixtureIgnoresInheritedSession(t *testing.T) {
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, "ses_20261020-000000-ffffffff")
	t.Setenv(mode.EnvRoom, "live-room")
	t.Setenv(mode.EnvRoomID, "live-room-id")
	f := newIssueFixture(t)
	out, err := f.run("Fixture issue", "--json")
	if err != nil {
		t.Fatalf("inherited session reached the fixture: %v; %s", err, out)
	}
	var issue room.Issue
	if err := json.Unmarshal([]byte(out), &issue); err != nil || issue.Filer != f.rec.Name {
		t.Fatalf("fixture identity: %+v, %v", issue, err)
	}
}
