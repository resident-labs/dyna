package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"log"
)

func TestRulesCommands(t *testing.T) {
	storetest.Isolate(t)
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(root, "sessions")}
	mgr.Rooms = rooms
	s := goalsvc.New()
	s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
	s.Notify = notify.New(rooms, mgr)
	actor := authority.Actor{Role: authority.Owner}
	serveMutations(t, s, &actor)
	id := "ses_20261018-000001-aaaaaaaa"
	recordtest.Write(t, mgr.Store.Root, id, subagent.Meta{ID: id, Name: "helper", RoomID: rec.ID, Room: rec.Name, Depth: 1, Resumable: true})
	call := func(args ...string) (string, error) {
		cmd := Command(Options{Service: func() *goalsvc.Service { return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := call("add", "Keep it simple", "--room", rec.Name, "--json"); err != nil || !strings.Contains(out, `"setter":"owner"`) {
		t.Fatal(out, err)
	}
	if out, err := call("--room", rec.Name, "--json"); err != nil || !strings.Contains(out, `"used":14,"max":3000`) {
		t.Fatal(out, err)
	}
	if out, err := call("list", "--room", rec.Name); err != nil || !strings.Contains(out, "r1  owner  just now  Keep it simple") {
		t.Fatal(out, err)
	}
	actor = authority.Actor{Role: authority.Session, Session: id}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, id)
	t.Setenv(mode.EnvRoom, rec.Name)
	t.Setenv(mode.EnvRoomID, rec.ID)
	if out, err := call("add", "Test changed packages", "--json"); err != nil || !strings.Contains(out, `"setterName":"helper"`) {
		t.Fatal(out, err)
	}
	for _, verb := range []string{"edit", "rm"} {
		args := []string{verb, "r1"}
		if verb == "edit" {
			args = append(args, "Changed")
		}
		if _, err := call(args...); err == nil || err.Error() != "r1 was written by the owner. Ask the owner to change it." {
			t.Fatal(err)
		}
	}
	if out, err := call("edit", "r2", "Run quick tests", "--json"); err != nil || !strings.Contains(out, `"oldText":"Test changed packages"`) {
		t.Fatal(out, err)
	}
	if _, err := call("add", "Wrong room", "--room", other.Name); err == nil || err.Error() != "use the current session's room" {
		t.Fatal(err)
	}
	if _, err := call("rm", "r2"); err != nil {
		t.Fatal(err)
	}
	actor = authority.Actor{Role: authority.Owner}
	storetest.ClearStamp(t)
	if _, err := call("edit", "r1", "Owner changed", "--room", rec.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := call("rm", "r1", "--room", rec.Name); err != nil {
		t.Fatal(err)
	}
	if out, err := call("--json", "--room", rec.Name); err != nil || !strings.Contains(out, `"rules":[],"used":0`) {
		t.Fatal(out, err)
	}
	msgs, err := rooms.Messages(rec.ID)
	if err != nil || len(msgs) != 6 {
		t.Fatal(msgs, err)
	}
	for _, msg := range msgs {
		if msg.Kind != "rule" || msg.Notice.Kind != "changed" || fmt.Sprint(msg.To) != "[work]" {
			t.Fatal(msg)
		}
	}
	if helper, _ := msgs[1].Rule(); helper.Actor != "work:helper" {
		t.Fatal(msgs)
	}
	if removed, _ := msgs[3].Rule(); removed.Actor != "work:helper" || removed.Verb != room.RuleRemoved {
		t.Fatal(msgs)
	}
	if out, err := call("add", "Use Central time", "--global", "--room", rec.Name); err != nil || out != "u1  Use Central time\n" {
		t.Fatal(out, err)
	}
	if out, err := call("list", "--global", "--room", other.Name); err != nil || !strings.Contains(out, "u1  owner  just now  Use Central time") {
		t.Fatal(out, err)
	}
	if out, err := call("--json", "--room", other.Name); err != nil || !strings.Contains(out, `"rules":[]`) {
		t.Fatal("a global rule showed as a room rule:", out, err)
	}
	recent, _ := rooms.Messages(rec.ID)
	if added, ok := recent[len(recent)-1].Rule(); !ok || added.Scope != "global" || added.Text != "Use Central time" {
		t.Fatal(recent[len(recent)-1])
	}
}

func TestRulesCommandValidation(t *testing.T) {
	for _, args := range [][]string{{"add"}, {"edit", "r1"}, {"rm"}, {"unexpected"}, {"list", "extra"}} {
		cmd := NewCommand()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted", args)
		}
	}
	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetArgs([]string{"--help"})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "engine checks the caller identity") {
		t.Fatal(out.String(), err)
	}
}

func TestRulesCommandsReturnSavedRuleWhenNoticeFails(t *testing.T) {
	storetest.Isolate(t)
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	feed := filepath.Join(rooms.Root, rec.ID, "messages.jsonl")
	if err := os.Remove(feed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(feed, 0700); err != nil {
		t.Fatal(err)
	}
	service := &goalsvc.Service{
		Store:  &goals.Store{Root: filepath.Join(root, "goals")},
		Notify: notify.New(rooms, &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}}),
	}
	actor := authority.Actor{Role: authority.Owner}
	serveMutations(t, service, &actor)
	var noticeLog bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&noticeLog)
	t.Cleanup(func() { log.SetOutput(previousLog) })
	for _, tc := range []struct {
		args  []string
		text  string
		count int
	}{
		{[]string{"add", "First rule"}, "First rule", 1},
		{[]string{"edit", "r1", "Changed rule"}, "Changed rule", 1},
		{[]string{"rm", "r1"}, "Changed rule", 0},
	} {
		cmd := Command(Options{Service: func() *goalsvc.Service { return service }})
		var out, warning bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&warning)
		cmd.SetArgs(append(tc.args, "--room", rec.Name, "--json"))
		if err := cmd.Execute(); err != nil {
			t.Fatal("saved change reported as failure", err)
		}
		var rule goals.Rule
		if err := json.Unmarshal(out.Bytes(), &rule); err != nil {
			t.Fatal("invalid JSON output", out.String(), err)
		}
		if rule.ID != "r1" || rule.Text != tc.text {
			t.Fatal(rule)
		}
		if !strings.Contains(noticeLog.String(), "rule r1 was saved, but its room notice failed:") {
			t.Fatal(noticeLog.String())
		}
		saved, err := service.Store.Rules(rec.ID)
		if err != nil || len(saved) != tc.count {
			t.Fatal(saved, err)
		}
		if tc.count != 0 && saved[0].Text != tc.text {
			t.Fatal(saved)
		}
	}
}
