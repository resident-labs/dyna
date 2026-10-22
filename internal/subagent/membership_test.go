package subagent

import (
	"reflect"
	"testing"
	"time"
)

func TestRoomMembershipOrdersAndFiltersTree(t *testing.T) {
	now := time.Now()
	all := []Info{
		{Meta: Meta{ID: "child", RoomID: "room", ParentSession: "parent", Created: now}},
		{Meta: Meta{ID: "parent", RoomID: "room", Created: now.Add(-time.Second)}},
		{Meta: Meta{ID: "orphan", RoomID: "room", ParentSession: "missing", Created: now}},
		{Meta: Meta{ID: "closed", RoomID: "room", Created: now}, Status: StatusClosed},
		{Meta: Meta{ID: "other", RoomID: "other", Created: now}},
	}
	got := RoomMembership(all, "room", false)
	ids := []string{}
	for _, info := range got.Sessions {
		ids = append(ids, info.ID)
	}
	if !reflect.DeepEqual(ids, []string{"parent", "child", "orphan"}) {
		t.Fatalf("sessions = %v", ids)
	}
	if len(got.Roots) != 2 || got.Roots[0].Info.ID != "parent" || len(got.Roots[0].Children) != 1 || got.Roots[0].Children[0].Info.ID != "child" || got.Roots[1].Info.ID != "orphan" {
		t.Fatalf("tree = %+v", got.Roots)
	}
	if len(RoomMembership(all, "room", true).Sessions) != 4 {
		t.Fatal("closed session missing")
	}
}

func TestRoomMembershipCutsCycles(t *testing.T) {
	all := []Info{{Meta: Meta{ID: "a", RoomID: "room", ParentSession: "b"}}, {Meta: Meta{ID: "b", RoomID: "room", ParentSession: "a"}}, {Meta: Meta{ID: "c", RoomID: "room", ParentSession: "c"}}}
	got := RoomMembership(all, "room", true)
	if len(got.Roots) != 2 || got.Roots[0].Info.ID != "a" || len(got.Roots[0].Children) != 1 || len(got.Roots[0].Children[0].Children) != 0 || got.Roots[1].Info.ID != "c" {
		t.Fatalf("cycle tree = %+v", got.Roots)
	}
}
