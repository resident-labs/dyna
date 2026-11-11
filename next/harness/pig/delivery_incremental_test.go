package pig

import (
	"context"
	"os"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// A check reads only what the session file gained since the last one, and still sees what the full read saw: a line left
// unfinished is not counted until it is whole, and a file that shrank is read again from its start.
func TestDeliveredReadsOnlyWhatWasAppended(t *testing.T) {
	h := open(t, pigtest.NewModel([]pigtest.Block{{Text: "ok"}}))
	start(t, h, "first")
	ctx := context.Background()
	delivered := func(seq int, want bool) {
		t.Helper()
		if done, err := h.Delivered(ctx, contract.DeliverySeq(seq)); err != nil || done != want {
			t.Fatalf("delivery %d: %v %v, want %v", seq, done, err, want)
		}
	}
	size := func() int64 {
		t.Helper()
		info, err := os.Stat(h.session.Path())
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	add := func(text string) {
		t.Helper()
		f, err := os.OpenFile(h.session.Path(), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	delivered(1, true)
	whole := size()
	if h.delivered.read != whole {
		t.Fatalf("read %d of %d bytes", h.delivered.read, whole)
	}
	const line = `{"type":"message","id":"x","parentId":null,"timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"late","deliveryId":"50"}}`
	add(line[:40])
	delivered(50, false)
	if h.delivered.read != whole {
		t.Fatalf("an unfinished line moved the offset to %d", h.delivered.read)
	}
	add(line[40:] + "\n")
	delivered(50, true)
	delivered(1, true)
	if h.delivered.read != size() {
		t.Fatalf("read %d of %d bytes", h.delivered.read, size())
	}
	// A line that only mentions the member in its text names no delivery.
	add(`{"type":"message","id":"y","parentId":"x","timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"assistant","content":"deliveryId 51"}}` + "\n")
	delivered(51, false)
	if err := os.Truncate(h.session.Path(), whole); err != nil {
		t.Fatal(err)
	}
	delivered(50, false)
	delivered(1, true)
}
