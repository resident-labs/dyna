package roomsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// row finds a member by session ID.
func row(members []Member, id string) Member {
	for _, m := range members {
		if m.ID == id {
			return m
		}
	}
	return Member{}
}

func TestRosterRebuildBatchesResponsibilityCounts(t *testing.T) {
	e := newEnv(t)
	firstRoom := e.room("batch-counts-a")
	secondRoom := e.room("batch-counts-b")
	first := e.start(firstRoom, "first", nil)
	second := e.start(secondRoom, "second", nil)

	calls := 0
	read := responsibilityCounts
	responsibilityCounts = func(manager *subagent.Manager, ctx context.Context, infos []subagent.Info) (map[string]map[string]int, error) {
		calls++
		if len(infos) != 2 {
			t.Errorf("one count pass received %d sessions, want 2", len(infos))
		}
		return read(manager, ctx, infos)
	}
	t.Cleanup(func() { responsibilityCounts = read })

	watch := newMemberWatch()
	e.svc.scanMembers([]subagent.Info{first, second}, []room.Record{firstRoom, secondRoom}, watch, nil, true, func(Event) {})
	if calls != 1 {
		t.Fatalf("two-room roster rebuild used %d responsibility count passes, want 1", calls)
	}
	for _, item := range []struct {
		room room.Record
		info subagent.Info
	}{{firstRoom, first}, {secondRoom, second}} {
		if got := row(watch.rooms[item.room.ID].items, item.info.ID).Responsibilities["goal"]; got != 1 {
			t.Errorf("%s counts %v goals, want 1", item.info.Address(), got)
		}
	}
}

// Each row names the computer it uses: the owner row names the room's root
// computer, and a session row names its own computer.
func TestMembersNameTheComputerEachRowUses(t *testing.T) {
	e := newEnv(t)
	rec := e.room("computer-ids")
	lead := e.start(rec, "lead", nil)

	members, err := e.svc.Members(rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := members[0].Computer, "root-"+rec.ID; got != want {
		t.Fatalf("owner computer = %q, want %q", got, want)
	}
	if got := row(members, lead.ID).Computer; got != lead.ID {
		t.Fatalf("lead computer = %q, want its session ID %q", got, lead.ID)
	}
}

// A single-member response names its computer too: session detail, reopen,
// profile and spawn build their member with the same builder as the roster.
func TestSingleMemberNamesItsComputer(t *testing.T) {
	e := newEnv(t)
	rec := e.room("single-computer")
	lead := e.start(rec, "lead", nil)

	got, err := e.svc.memberByID(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Computer != lead.ID {
		t.Fatalf("session computer = %q, want its session ID %q", got.Computer, lead.ID)
	}
}

// A member row shows what stops its session from finishing: the owner row keeps
// no count, and a new child counts for its parent.
func TestMembersCountWhatStopsAnOpenSession(t *testing.T) {
	e := newEnv(t)
	rec := e.room("counts")
	lead := e.start(rec, "lead", nil)

	members, err := e.svc.Members(rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if members[0].Owner != true || members[0].Responsibilities != nil {
		t.Fatalf("the owner row carries a count: %+v", members[0])
	}
	if got := row(members, lead.ID).Responsibilities["goal"]; got != 1 {
		t.Fatalf("the lead counts %v goals, want 1", got)
	}

	e.start(rec, "child", &lead)
	fresh, err := e.svc.Members(rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := row(fresh, lead.ID).Responsibilities["child"]; got != 1 {
		t.Fatalf("the lead counts %v children, want 1", got)
	}

	// Open leaves advisory counts to the subsequent Members request.
	opened, err := e.svc.Open(rec.ID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := row(opened.Members, lead.ID).Responsibilities; len(got) != 0 {
		t.Fatalf("Open computed advisory counts: %v", got)
	}
}
