package listpage

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCutoffAndCursor(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	items := []Meta{{ID: "open", Open: true, At: now.Add(-30 * Recent)}, {ID: "recent", At: now}, {ID: "old-a", At: now.Add(-2 * Recent)}, {ID: "old-b", At: now.Add(-2 * Recent)}}
	meta := func(m Meta) Meta { return m }
	first, err := Select(items, "", 1, now, meta)
	if err != nil || len(first.Items) != 2 || !first.Older || first.Cursor == "" {
		t.Fatalf("default: %+v, %v", first, err)
	}
	seen := map[string]bool{}
	for _, row := range first.Items {
		seen[row.ID] = true
	}
	cursor := first.Cursor
	for n := 0; n < 3; n++ {
		page, err := Select(items, cursor, 1, now.Add(time.Hour), meta)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Items {
			if seen[row.ID] {
				t.Fatalf("duplicate %s", row.ID)
			}
			seen[row.ID] = true
		}
		if !page.Older {
			break
		}
		cursor = page.Cursor
	}
	if len(seen) != len(items) {
		t.Fatalf("lost history: %v", seen)
	}
}

// The first page drops items past the cutoff before it sorts, so a long
// history costs one pass, not a sort.
func TestFirstPageSkipsAgedItemsWithoutSorting(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	items := []Meta{{ID: "open", Open: true, At: now.Add(-30 * Recent)}, {ID: "b", At: now.Add(-time.Hour)}, {ID: "a", At: now.Add(-time.Minute)}}
	for n := 0; n < 2000; n++ {
		items = append(items, Meta{ID: "old" + strconv.Itoa(n), At: now.Add(-2*Recent - time.Duration(n)*time.Second)})
	}
	calls := 0
	page, err := Select(items, "", 0, now, func(m Meta) Meta { calls++; return m })
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range page.Items {
		ids = append(ids, m.ID)
	}
	if got := strings.Join(ids, ","); got != "open,a,b" || !page.Older || page.Cursor == "" {
		t.Fatalf("page %q older=%v cursor=%q", got, page.Older, page.Cursor)
	}
	// Its cursor continues after the last shown item.
	next, err := Select(items, page.Cursor, 1, now, func(m Meta) Meta { return m })
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "old0" {
		t.Fatalf("next page %+v, %v", next, err)
	}
	if calls > 2*len(items) {
		t.Fatalf("%d meta calls for %d items", calls, len(items))
	}
}

func TestPageOfManyRecentItemsKeepsNewestInOrder(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	var items []Meta
	for n := 0; n < 3000; n++ {
		// Repeated times make the ID tie-break matter.
		items = append(items, Meta{ID: "job" + strconv.Itoa(n), At: now.Add(-time.Duration(n%1000) * time.Second)})
	}
	items = append(items, Meta{ID: "open", Open: true, At: now})
	calls := 0
	page, err := Select(items, "", 3, now, func(m Meta) Meta { calls++; return m })
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range page.Items {
		ids = append(ids, m.ID)
	}
	if got := strings.Join(ids, ","); got != "open,job0,job1000,job2000" || !page.Older || page.Cursor == "" {
		t.Fatalf("page %q older=%v cursor=%q", got, page.Older, page.Cursor)
	}
	next, err := Select(items, page.Cursor, 2, now, func(m Meta) Meta { return m })
	if err != nil || len(next.Items) != 2 || next.Items[0].ID != "job1" || next.Items[1].ID != "job1001" {
		t.Fatalf("next page %+v, %v", next, err)
	}
	// The sort called meta about n log n times; one pass calls it once per item.
	if calls > len(items) {
		t.Fatalf("%d meta calls for %d items", calls, len(items))
	}
}

func TestRejectInvalidCursor(t *testing.T) {
	for _, before := range []string{"not a cursor", "e30", "bnVsbA"} {
		if _, err := Select([]Meta{}, before, 1, time.Now(), func(m Meta) Meta { return m }); err == nil {
			t.Errorf("accepted invalid cursor %q", before)
		}
	}
}
