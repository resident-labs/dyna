package authority

import "testing"

func TestMergeControlRequiresOwnRoomRelation(t *testing.T) {
	for _, role := range []Role{Owner, Root, Session, Worker} {
		for _, sameRoom := range []bool{false, true} {
			err := Check("git merge resume", Actor{Role: role}, func(Relation) bool { return sameRoom })
			want := role == Owner || role == Root && sameRoom
			if (err == nil) != want {
				t.Errorf("role=%s sameRoom=%v: %v", role, sameRoom, err)
			}
		}
	}
}
