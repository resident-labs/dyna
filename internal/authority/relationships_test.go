package authority

import "testing"

func TestObjectRelationsRequireSession(t *testing.T) {
	old := Rules
	Rules = append(append([]Rule(nil), old...), Rule{Action: "test object", Relations: []Relation{Author, Parent, Self, Librarian}})
	defer func() { Rules = old }()
	for _, role := range []Role{Owner, Root, Session, Worker, Role("steward"), ""} {
		err := Check("test object", Actor{Role: role, Session: "matching-id"}, func(Relation) bool { return true })
		if (err == nil) != (role == Session) {
			t.Errorf("role %q: %v", role, err)
		}
	}
}

func TestSessionCannotCloseItself(t *testing.T) {
	if err := Check("subagent close", Actor{Role: Session, Session: "self"}, func(Relation) bool { return false }); err == nil {
		t.Fatal("self-close allowed")
	}
	if err := Check("subagent close", Actor{Role: Session, Session: "parent"}, func(Relation) bool { return true }); err != nil {
		t.Fatal(err)
	}
}
