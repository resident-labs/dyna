package view

import (
	"dyna-agent/internal/pr/watch"
	"strings"
	"testing"
	"time"
)

func TestWorkerInputsUseFactsWithoutSelfWaking(t *testing.T) {
	yes, no := true, false
	r := &watch.Record{Key: "pr", RoomID: "room", Head: "head", CI: "passed", State: "open", Watching: true, Mergeable: &yes}
	st := watch.State{Records: map[string]*watch.Record{"pr": r}}
	inputs := func() string { return prWorkerInputs(st, "room", []*watch.Record{r}, nil, nil) }
	baseline := inputs()
	copy := *r
	for _, tc := range []struct {
		name   string
		change func(*watch.Record)
	}{
		{"head", func(r *watch.Record) { r.Head = "new" }},
		{"unverified", func(r *watch.Record) { r.HeadStale = true }},
		{"CI", func(r *watch.Record) { r.CI = "pending" }},
		{"draft", func(r *watch.Record) { r.IsDraft = true }},
		{"mergeable", func(r *watch.Record) { r.Mergeable = &no }},
		{"closed", func(r *watch.Record) { r.State = "merged" }},
		{"approval head", func(r *watch.Record) { r.RoundHead = "head" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*r = copy
			tc.change(r)
			if inputs() == baseline {
				t.Fatal("fact change did not wake worker")
			}
		})
	}
	*r = copy
	r.HeadVerifiedAt = time.Now()
	r.Checked = time.Now()
	r.Polling.Next = time.Now().Add(time.Minute)
	r.ChecksAt = time.Now()
	r.Notices = []watch.Notice{{ID: "notice", Done: true, PostID: "post"}}
	if inputs() != baseline {
		t.Fatal("worker output woke itself")
	}
}

// A record's text is made again only when the scan reports it written. The
// other records of the room keep theirs, so a busy room is not marshaled whole
// for each write.
func TestWorkerInputsRebuildOnlyWrittenRecords(t *testing.T) {
	a := &watch.Record{Key: "a", RoomID: "room", Head: "a1", State: "open", Summary: strings.Repeat("large worker input ", 100)}
	b := &watch.Record{Key: "b", RoomID: "room", Head: "b1", State: "open"}
	st := watch.State{Records: map[string]*watch.Record{"a": a, "b": b}}
	records := []*watch.Record{b, a}
	memo := map[string]string{}
	all := func(string) bool { return true }
	baseline := prWorkerInputs(st, "room", records, memo, all)
	if got := len(memo[inputPrefix+"a"]); got != 64 {
		t.Fatalf("one record's memo retained %d bytes; want a fixed-size SHA-256 digest", got)
	}
	aDigest, bDigest := memo[inputPrefix+"a"], memo[inputPrefix+"b"]
	a.Head, b.Head = "a2", "b2"
	if got := prWorkerInputs(st, "room", records, memo, func(key string) bool { return key == "a" }); got == baseline || memo[inputPrefix+"a"] == aDigest || memo[inputPrefix+"b"] != bDigest {
		t.Fatal("only the written record's digest should change")
	}
	if got := prWorkerInputs(st, "room", records, nil, nil); got != prWorkerInputs(st, "room", records, memo, all) {
		t.Fatalf("a memo changed the text:\n%s", got)
	}
}
