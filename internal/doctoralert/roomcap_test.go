package doctoralert

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
)

var capT0 = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
var errSend = errors.New("send failed")

// fakeCapTree writes one room slice below a user manager root, whose only
// cap is a memory.max of 100. setUse rewrites memory.current, as the kernel's
// counters move between passes.
func fakeCapTree(t *testing.T, current uint64) (string, func(uint64)) {
	t.Helper()
	root := t.TempDir()
	slice := filepath.Join(root, "dyna.slice", "dyna-room.slice", agentslice.RoomSlice("room_1"))
	if err := os.MkdirAll(slice, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"memory.high": "max\n", "memory.max": "100\n", "pids.max": "max\n",
	} {
		if err := os.WriteFile(filepath.Join(slice, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setUse := func(use uint64) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(slice, "memory.current"), []byte(fmt.Sprintf("%d\n", use)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setUse(current)
	return root, setUse
}

// fakeCaps is one room whose only cap is a memory.max of 100, read from a
// fake cgroup tree.
func fakeCaps(t *testing.T, current uint64) (*CapNotifier, *[]CapNote, *time.Time, func(uint64)) {
	t.Helper()
	now := capT0
	root, setUse := fakeCapTree(t, current)
	var sent []CapNote
	n := &CapNotifier{
		DataDir: t.TempDir(),
		Now:     func() time.Time { return now },
		Read:    func() ([]doctor.RoomUse, error) { return doctor.RoomCaps(root), nil },
		Trees: func(roomID string) doctor.CapTrees {
			return doctor.CapTrees{
				ByMemory: []doctor.CapTree{{Command: "pi", PID: 10, Memory: 90, Count: 12}},
				ByCount:  []doctor.CapTree{{Command: "pi", PID: 10, Memory: 90, Count: 12}},
			}
		},
		Send: func(note CapNote) error { sent = append(sent, note); return nil },
	}
	return n, &sent, &now, setUse
}

// D-353: a room hears once when a cap crosses 80%, not again while it stays
// high, and only again after it fell under 70% and crossed once more.
func TestCapAlertsOncePerCrossingWithHysteresis(t *testing.T) {
	n, sent, now, setUse := fakeCaps(t, 79)
	if err := n.Alert(); err != nil || len(*sent) != 0 {
		t.Fatalf("79%% of the cap sent %d, %v", len(*sent), err)
	}
	setUse(81)
	if err := n.Alert(); err != nil || len(*sent) != 1 {
		t.Fatalf("81%% of the cap sent %d, %v", len(*sent), err)
	}
	note := (*sent)[0]
	if note.RoomID != "room_1" || note.Cap != doctor.CapMemoryMax || note.Current != 81 || note.Limit != 100 {
		t.Fatalf("note = %+v", note)
	}
	if !note.At.Equal(capT0) || len(note.Trees.ByMemory) != 1 || note.Trees.ByMemory[0].Command != "pi" {
		t.Fatalf("note = %+v", note)
	}
	*now = now.Add(time.Minute)
	setUse(90)
	if err := n.Alert(); err != nil || len(*sent) != 1 {
		t.Fatalf("a second pass above the cap sent %d, %v", len(*sent), err)
	}
	*now = now.Add(time.Minute)
	setUse(69)
	if err := n.Alert(); err != nil || len(*sent) != 1 {
		t.Fatalf("falling under 70%% sent %d, %v", len(*sent), err)
	}
	*now = now.Add(time.Minute)
	setUse(75)
	if err := n.Alert(); err != nil || len(*sent) != 1 {
		t.Fatalf("the hysteresis band sent %d, %v", len(*sent), err)
	}
	*now = now.Add(time.Minute)
	setUse(81)
	if err := n.Alert(); err != nil || len(*sent) != 2 {
		t.Fatalf("crossing 80%% again after a re-arm sent %d, %v", len(*sent), err)
	}
	if (*sent)[1].At.Equal(capT0) {
		t.Fatalf("the second alert kept the first time: %+v", (*sent)[1])
	}
}

// A cap between the two thresholds, with no alert active, alerts nothing; a
// send that fails is retried at the next pass because only a sent note is
// recorded.
func TestCapAlertRetriesAFailedSend(t *testing.T) {
	n, sent, _, setUse := fakeCaps(t, 85)
	n.Send = func(note CapNote) error { return errSend }
	setUse(95)
	if err := n.Alert(); err == nil || !strings.Contains(err.Error(), "room_1") {
		t.Fatalf("a failed send returned %v", err)
	}
	if err := n.Alert(); err == nil {
		t.Fatal("the second pass must try again")
	}
	n.Send = func(note CapNote) error { *sent = append(*sent, note); return nil }
	if err := n.Alert(); err != nil || len(*sent) != 1 {
		t.Fatalf("after the retry %d sent, %v", len(*sent), err)
	}
}

// One room near two caps hears one note per cap, and a room with no caps
// hears nothing.
func TestCapAlertSendsOneNotePerCap(t *testing.T) {
	n, sent, _, _ := fakeCaps(t, 0)
	both := []doctor.CapUse{
		{Cap: doctor.CapMemoryMax, Current: 90, Limit: 100},
		{Cap: doctor.CapPidsMax, Current: 9, Limit: 10},
	}
	n.Read = func() ([]doctor.RoomUse, error) {
		return []doctor.RoomUse{
			{RoomID: "room_1", Caps: both},
			{RoomID: "room_2", Caps: nil},
		}, nil
	}
	if err := n.Alert(); err != nil || len(*sent) != 2 {
		t.Fatalf("two caps sent %d, %v", len(*sent), err)
	}
	if (*sent)[0].Cap != doctor.CapMemoryMax || (*sent)[1].Cap != doctor.CapPidsMax {
		t.Fatalf("notes = %+v", *sent)
	}
}

// A cap note reaches the room's root and the owner as one typed machine
// event: the root wakes, and the text names the cap, its sizes and the trees.
func TestPostCapNoteWakesRootWithCapAndTrees(t *testing.T) {
	store := &room.Store{Root: t.TempDir()}
	rec, err := store.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	note := CapNote{
		RoomID: rec.ID, Cap: doctor.CapMemoryMax, Current: 9 << 30, Limit: 10 << 30,
		Trees: doctor.CapTrees{
			ByMemory: []doctor.CapTree{{Command: "pi", PID: 42, Memory: 6 << 30, Count: 30}},
			ByCount:  []doctor.CapTree{{Command: "bash", PID: 50, Memory: 100 << 20, Count: 40}},
		},
		At: capT0,
	}
	if err := PostCapNote(store, note); err != nil {
		t.Fatal(err)
	}
	posts, err := store.Messages(rec.ID)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts = %d, %v", len(posts), err)
	}
	post := posts[0]
	if post.Kind != "machine" || post.Notice.Kind != "room-cap" || !post.Wake {
		t.Fatalf("post = %+v", post)
	}
	if !strings.Contains(post.Text, "Room work is near its memory.max cap: 9 GiB of 10 GiB") {
		t.Fatalf("text = %q", post.Text)
	}
	if !strings.Contains(post.Text, "pi (pid 42)") || !strings.Contains(post.Text, "bash (pid 50)") {
		t.Fatalf("text names no trees: %q", post.Text)
	}
	// The same crossing posted again is one post, not two.
	if err := PostCapNote(store, note); err != nil {
		t.Fatal(err)
	}
	if posts, err = store.Messages(rec.ID); err != nil || len(posts) != 1 {
		t.Fatalf("a retried note added a post: %d, %v", len(posts), err)
	}
}
