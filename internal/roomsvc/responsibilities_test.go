package roomsvc

import "testing"

// row finds a member by session ID.
func row(members []Member, id string) Member {
	for _, m := range members {
		if m.ID == id {
			return m
		}
	}
	return Member{}
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

	// Open fills the same counts as Members, so a room's first screen and its
	// later member reloads agree.
	opened, err := e.svc.Open(rec.ID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := row(opened.Members, lead.ID).Responsibilities, row(fresh, lead.ID).Responsibilities; len(got) != len(want) || got["child"] != want["child"] {
		t.Fatalf("Open counts %v, Members counts %v", got, want)
	}
}
