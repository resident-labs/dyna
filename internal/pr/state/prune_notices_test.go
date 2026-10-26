package state

import (
	"context"
	"testing"
	"time"
)

func TestPruneDoneNoticesKeepsPendingAndRecentAndStampsOldOnes(t *testing.T) {
	root := newRoot(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	st := bigState(1)
	key := "github.com/acme/widgets#0"
	st.Records[key].Notices = []Notice{
		{ID: "pending"},
		{ID: "recent", Done: true, DoneAt: now.Add(-time.Hour)},
		{ID: "old", Done: true, DoneAt: now.Add(-15 * 24 * time.Hour)},
		{ID: "unstamped", Done: true},
	}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	n, err := PruneDoneNotices(context.Background(), root, now)
	if err != nil || n != 1 {
		t.Fatalf("PruneDoneNotices removed %d, %v; want only the old delivered notice", n, err)
	}
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, notice := range got.Records[key].Notices {
		ids = append(ids, notice.ID)
		if notice.ID == "unstamped" && !notice.DoneAt.Equal(now) {
			t.Fatalf("unstamped notice got DoneAt %v, want %v", notice.DoneAt, now)
		}
	}
	if len(ids) != 3 || ids[0] != "pending" || ids[1] != "recent" || ids[2] != "unstamped" {
		t.Fatalf("notices left = %v", ids)
	}
	// A second pass finds nothing more to change.
	if n, err := PruneDoneNotices(context.Background(), root, now); err != nil || n != 0 {
		t.Fatalf("second pass removed %d, %v", n, err)
	}
}
