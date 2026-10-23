package goal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalservice"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestCommandsAndSelectors(t *testing.T) {
	storetest.Isolate(t)
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(root, "sessions")}
	mgr.Rooms = rooms
	s := goalservice.New()
	s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
	s.Notify = notify.New(rooms, mgr)
	actor := authority.Actor{Role: authority.Owner}
	serveMutations(t, s, &actor)
	ids := []string{}
	for i, name := range []string{"lead", "child", "peer"} {
		id := fmt.Sprintf("ses_20261017-00000%d-aaaaaaaa", i+1)
		ids = append(ids, id)
		meta := subagent.Meta{ID: id, Name: name, RoomID: rec.ID, Room: rec.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture"}
		if i == 1 {
			meta.ParentSession = ids[0]
			meta.Depth = 2
		}
		recordtest.Write(t, mgr.Store.Root, id, meta)
	}
	for _, tc := range []struct {
		by        string
		selectors []string
		ids       []string
		shared    bool
	}{
		{"owner", nil, []string{}, true}, {ids[0], nil, []string{ids[0]}, false}, {ids[0], []string{"@children"}, []string{ids[1]}, false}, {ids[0], []string{"@siblings"}, []string{ids[2]}, false}, {ids[0], []string{"@peer", "@peer"}, []string{ids[2]}, false}, {ids[0], []string{"@room"}, []string{}, true},
	} {
		h, sh, err := s.ResolveHolders(rec.ID, tc.by, tc.selectors)
		if err != nil || fmt.Sprint(h) != fmt.Sprint(tc.ids) || sh != tc.shared {
			t.Fatal(h, sh, err, tc)
		}
	}
	for _, sel := range [][]string{{"@missing"}, {"@room", "@peer"}, {"peer"}} {
		if _, _, err := s.ResolveHolders(rec.ID, ids[0], sel); err == nil {
			t.Fatal("accepted", sel)
		}
	}
	call := func(args ...string) (string, error) {
		calls := 0
		cmd := Command(Options{Service: func() *goalservice.Service { calls++; return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append(args, "--room", rec.Name))
		err := cmd.Execute()
		if calls > 1 {
			t.Fatalf("command initialized %d services; want no background service initialization", calls)
		}
		return out.String(), err
	}
	if out, err := call("Ship the relay", "--json"); err != nil || !strings.Contains(out, `"id":"g1"`) {
		t.Fatal(out, err)
	}
	actor = authority.Actor{Role: authority.Session, Session: ids[0]}
	if out, err := call("Test reconnect", "--for", "@child", "--json"); err != nil || !strings.Contains(out, ids[1]) {
		t.Fatal(out, err)
	}
	created, err := s.Store.Get(rec.ID, "g2")
	if err != nil || created.Setter != ids[0] {
		t.Fatalf("authenticated setter = %q, %v", created.Setter, err)
	}
	if out, err := call("show", "g2", "--json"); err != nil || strings.Contains(out, `"parent"`) {
		t.Fatal(out, err)
	}
	if out, err := call("done", "g1", "Shipped"); err != nil || out != "g1 done\n" {
		t.Fatalf("done output = %q, %v", out, err)
	}
	if out, err := call("done", "g1", "Shipped", "--json"); err != nil {
		t.Fatal(err)
	} else {
		var got goals.Goal
		if err := json.Unmarshal([]byte(out), &got); err != nil || got.ID != "g1" || got.State != "done" || got.Done == nil || got.Done.Summary != "Shipped" || got.Done.By != ids[0] {
			t.Fatalf("JSON done output = %q, %v", out, err)
		}
	}
	if _, err := call("edit", "g1", "--for", "@peer"); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Store.Get(rec.ID, "g1")
	if g.State != "active" || !g.HeldBy(ids[2]) || g.Edits[len(g.Edits)-1].By != ids[0] {
		t.Fatal(g)
	}
	if out, err := call("drop", "g2"); err != nil || out != "g2 dropped\n" {
		t.Fatalf("drop output = %q, %v", out, err)
	}
	dropped, err := s.Store.Get(rec.ID, "g2")
	if err != nil || dropped.Done == nil || dropped.Done.By != ids[0] {
		t.Fatalf("drop = %+v, %v", dropped, err)
	}
	if out, err := call("--all"); err != nil || !strings.Contains(out, "g2") {
		t.Fatal(out, err)
	}
	if _, err := call("unquoted", "text"); err == nil {
		t.Fatal("accepted multiple text arguments")
	}
}

func TestListAndCommandWords(t *testing.T) {
	storetest.Isolate(t)
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(root, "sessions")}
	mgr.Rooms = rooms
	s := goalservice.New()
	s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
	s.Notify = notify.New(rooms, mgr)
	actor := authority.Actor{Role: authority.Owner}
	serveMutations(t, s, &actor)
	call := func(args ...string) (string, error) {
		calls := 0
		cmd := Command(Options{Service: func() *goalservice.Service { calls++; return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--room", rec.Name}, args...))
		err := cmd.Execute()
		if calls > 1 {
			t.Fatalf("command initialized %d services; want no background service initialization", calls)
		}
		return out.String(), err
	}
	active, err := s.Store.Add(rec.ID, "owner", goals.Input{Text: "Ship the relay", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.Store.Add(rec.ID, "owner", goals.Input{Text: "Test reconnect", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Finish(rec.ID, closed.ID, "owner", "Tested", false); err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("list/all=%t", all), func(t *testing.T) {
			var flags []string
			if all {
				flags = []string{"--all"}
			}
			want, err := call(flags...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := call(append([]string{"list"}, flags...)...)
			if err != nil || got != want {
				t.Fatalf("list = %q, %v; no-arg listing = %q", got, err, want)
			}
			if !strings.Contains(got, active.Text) || strings.Contains(got, closed.Text) != all {
				t.Fatalf("unexpected goal visibility: %q", got)
			}
		})
	}
	other, err := s.Store.Add(rec.ID, "ses_other", goals.Input{Text: "Another lead's task", Holders: []string{"ses_other"}})
	if err != nil {
		t.Fatal(err)
	}
	sections, err := call()
	yoursAt, othersAt := strings.Index(sections, "Your goals:"), strings.Index(sections, "Other open goals in this room:")
	otherAt := strings.Index(sections, other.Text)
	if err != nil || yoursAt < 0 || othersAt < yoursAt || otherAt < othersAt || strings.Index(sections, active.Text) > othersAt {
		t.Fatalf("sections = %q, %v", sections, err)
	}
	var split struct{ Yours, Others []goals.Goal }
	if out, err := call("list", "--json"); err != nil || json.Unmarshal([]byte(out), &split) != nil || len(split.Yours) != 1 || len(split.Others) != 1 || split.Others[0].ID != other.ID {
		t.Fatalf("json = %q, %v", out, err)
	}
	if _, err := call("list", "extra"); err == nil {
		t.Fatal("list accepted an extra argument")
	}
	for _, word := range []string{"list", "ls", "show", "done", "drop", "edit", "help", "status", "watch"} {
		t.Run("reserved/"+word, func(t *testing.T) {
			// Spaces keep Cobra from dispatching a subcommand, and test the text guard.
			for _, text := range []string{" " + word + " ", strings.ToUpper(word)} {
				if _, err := call(text); err == nil || !strings.Contains(err.Error(), "dyna goal --help") {
					t.Fatalf("goal %q: want command hint, got %v", text, err)
				}
			}
			// Exact subcommand names must dispatch or fail without creating a goal.
			_, _ = call(word)
		})
	}
	records, err := s.Store.List(rec.ID)
	if err != nil || len(records) != 3 {
		t.Fatalf("listing or command words changed the store: %v, %v", records, err)
	}
	for _, text := range []string{"Launch", "list the release tasks", "show the release status"} {
		out, err := call(text, "--json")
		if err != nil || !strings.Contains(out, `"text":"`+text+`"`) {
			t.Fatalf("goal %q = %q, %v", text, out, err)
		}
	}
}

func TestDaemonCommandRemoved(t *testing.T) {
	for _, cmd := range NewCommand().Commands() {
		if cmd.Name() == "watch" {
			t.Fatalf("daemon command %q remains registered", cmd.Name())
		}
	}
}
