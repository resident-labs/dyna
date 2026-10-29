package state

import (
	"reflect"
	"testing"
)

func TestReadHeldCommentsDoesNotCloneTheWholeState(t *testing.T) {
	root := newRoot(t)
	st := bigState(20)
	held := HeldComment{ID: "issue-comments:1", Commenter: "outside", Body: "held text", Version: "v1"}
	st.Records["github.com/acme/widgets#3"].HeldComments = map[string]HeldComment{held.ID: held}
	st.Records["github.com/acme/widgets#4"].RoomID = "other"
	st.Records["github.com/acme/widgets#4"].HeldComments = map[string]HeldComment{held.ID: held}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	copies := 0
	original := copyForRead
	copyForRead = func(st State) State { copies++; return original(st) }
	t.Cleanup(func() { copyForRead = original })
	got, err := ReadHeldComments(root, "room")
	if err != nil {
		t.Fatal(err)
	}
	if copies != 0 {
		t.Fatalf("held comment read cloned the whole state %d times", copies)
	}
	want := []HeldCommentRef{{Key: "github.com/acme/widgets#3", RoomID: "room", Number: 3, Held: held}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("held comments: %+v, want %+v", got, want)
	}
}
