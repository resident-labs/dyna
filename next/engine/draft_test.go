package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// draftRig is a goal rig with the child's turn running.
type draftRig struct {
	*goalRig
	turn contract.TurnID
}

func newDraftRig(t *testing.T) *draftRig {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.notice(r.child)
	return &draftRig{goalRig: r, turn: r.begin(r.child)}
}

func (r *draftRig) stream(caller contract.Principal, room contract.RoomID, watch contract.SessionID) <-chan contract.Event {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.ctx)
	r.t.Cleanup(cancel)
	after := r.headSeq()
	ch, e := r.s.Events(ctx, caller, room, after, watch)
	if e != nil {
		r.t.Fatal(e)
	}
	return ch
}

func (r *draftRig) headSeq() int64 {
	r.t.Helper()
	var seq int64
	if err := r.st.Read().QueryRow("SELECT COALESCE(MAX(seq),0) FROM events").Scan(&seq); err != nil {
		r.t.Fatal(err)
	}
	return seq
}

func (r *draftRig) send(draft contract.Draft) {
	r.t.Helper()
	if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: r.turn, Draft: &draft}); e != nil {
		r.t.Fatal(e)
	}
}

// nextDraft reads the stream up to its next turn.draft, which it fails without.
func nextDraft(t *testing.T, ch <-chan contract.Event) contract.TurnDraft {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-ch:
			if body, ok := event.Body.(contract.TurnDraft); ok {
				if event.Seq != 0 {
					t.Fatalf("a draft has seq %d", event.Seq)
				}
				return body
			}
		case <-timeout:
			t.Fatal("no draft")
		}
	}
}

// noDraft fails if a turn.draft arrives within a moment.
func noDraft(t *testing.T, ch <-chan contract.Event, who string) {
	t.Helper()
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case event := <-ch:
			if _, ok := event.Body.(contract.TurnDraft); ok {
				t.Fatalf("%s got a draft", who)
			}
		case <-timeout:
			return
		}
	}
}

var draftStarted = time.UnixMilli(1_800_000_000_000).UTC()

// A draft reaches the owner's stream that watches its session, and a stream that starts watching mid-call gets what is
// buffered first, then the edits after it.
func TestADraftReachesTheOwnersWatchingStream(t *testing.T) {
	r := newDraftRig(t)
	watching := r.stream(ownerCaller, r.room, r.child)
	r.send(contract.Draft{Message: 1, Kind: contract.DraftReply, Started: draftStarted, Text: &contract.DraftEdit{Text: "Héllo 👋 "}})
	if got := nextDraft(t, watching); got.Session != r.child || got.Turn != r.turn || got.Draft.Text.Text != "Héllo 👋 " || !got.Draft.Started.Equal(draftStarted) {
		t.Fatalf("got %+v", got)
	}
	// From counts UTF-16 units: "Héllo 👋 " is 9.
	r.send(contract.Draft{Message: 1, Kind: contract.DraftReply, Started: draftStarted, Text: &contract.DraftEdit{From: 9, Text: "world "}, Reasoning: &contract.DraftEdit{Text: "greeting"}})
	nextDraft(t, watching)
	late := r.stream(ownerCaller, r.room, r.child)
	want := contract.TurnDraft{Session: r.child, Turn: r.turn, Draft: contract.Draft{Message: 1, Kind: contract.DraftReply, Started: draftStarted, Text: &contract.DraftEdit{Text: "Héllo 👋 world "}, Reasoning: &contract.DraftEdit{Text: "greeting"}}}
	if got := nextDraft(t, late); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot %+v, want %+v", got, want)
	}
	// A new call replaces the draft; an edit of the call before it is stale.
	r.send(contract.Draft{Message: 2, Kind: contract.DraftCompaction, Started: draftStarted.Add(time.Second), Text: &contract.DraftEdit{Text: "## Summary "}})
	r.send(contract.Draft{Message: 1, Kind: contract.DraftReply, Started: draftStarted, Text: &contract.DraftEdit{From: 15, Text: "late "}})
	nextDraft(t, late)
	noDraft(t, late, "a stream after a stale edit")
	want = contract.TurnDraft{Session: r.child, Turn: r.turn, Draft: contract.Draft{Message: 2, Kind: contract.DraftCompaction, Started: draftStarted.Add(time.Second), Text: &contract.DraftEdit{Text: "## Summary "}}}
	if got := nextDraft(t, r.stream(ownerCaller, r.room, r.child)); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot %+v, want %+v", got, want)
	}
	// The turn's end forgets it, so a stream that watches afterwards starts empty.
	r.end(r.child, r.turn, contract.TurnCompleted, "", "done")
	noDraft(t, r.stream(ownerCaller, r.room, r.child), "a stream after the turn ended")
}

// No agent ever sees a draft: a session cannot watch, and the streams that do not watch, the session's own among them,
// carry none. Nothing is delivered either.
func TestADraftReachesNoAgentAndNoOtherStream(t *testing.T) {
	r := newDraftRig(t)
	for _, caller := range []contract.Principal{asSession(r.child), asSession(r.lead), asSession(r.root)} {
		if _, e := r.s.Events(r.ctx, caller, r.room, 0, r.child); e == nil || e.Code != contract.ErrForbidden {
			t.Fatalf("%s watched: %+v", caller.Session, e)
		}
	}
	if _, e := r.s.Events(r.ctx, ownerCaller, "", 0, r.child); e == nil {
		t.Fatal("the owner stream of every room watched a session")
	}
	var deliveries int
	count := func() int {
		var n int
		if err := r.st.Read().QueryRow("SELECT COUNT(*) FROM deliveries").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deliveries = count()
	streams := map[string]<-chan contract.Event{
		"the child's room stream":                     r.stream(asSession(r.child), r.room, ""),
		"the lead's room stream":                      r.stream(asSession(r.lead), r.room, ""),
		"the root's room stream":                      r.stream(asSession(r.root), r.room, ""),
		"the owner's room stream":                     r.stream(ownerCaller, r.room, ""),
		"the owner's global":                          r.stream(ownerCaller, "", ""),
		"a stream watching another":                   r.stream(ownerCaller, r.room, r.lead),
		"a stream watching a session not in the room": r.stream(ownerCaller, r.room, "ses_missing"),
	}
	watching := r.stream(ownerCaller, r.room, r.child)
	r.send(contract.Draft{Message: 1, Kind: contract.DraftReply, Started: draftStarted, Text: &contract.DraftEdit{Text: "secret plan "}})
	nextDraft(t, watching)
	for who, ch := range streams {
		noDraft(t, ch, who)
	}
	if got := count(); got != deliveries {
		t.Fatalf("deliveries went from %d to %d", deliveries, got)
	}
}

// A draft is never written: no event row, no turn update, and its text is in none of the store's files.
func TestADraftWritesNothingToTheStore(t *testing.T) {
	r := newDraftRig(t)
	watching := r.stream(ownerCaller, r.room, r.child)
	var active int64
	state := func() (int64, int64) {
		if err := r.st.Read().QueryRow("SELECT active_at FROM turns WHERE id=?", r.turn).Scan(&active); err != nil {
			t.Fatal(err)
		}
		return r.headSeq(), active
	}
	seq, at := state()
	r.advance(5 * time.Minute) // past MarkTurnActive's minute, so a label would write
	const marker = "zq-draft-marker-7f3a"
	for i := range 20 {
		r.send(contract.Draft{Message: 1, Kind: contract.DraftReply, Started: draftStarted, Reasoning: &contract.DraftEdit{From: i * len(marker), Text: marker}})
		nextDraft(t, watching)
	}
	if s, a := state(); s != seq || a != at {
		t.Fatalf("events head %d→%d, active_at %d→%d", seq, s, at, a)
	}
	if err := r.st.Close(); err != nil {
		t.Fatal(err)
	}
	r.open() // the rig's cleanup closes this one
	err := filepath.WalkDir(r.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(marker)) {
			t.Errorf("%s holds the draft", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A draft of a turn that is not running is dropped without an error, so the box does not retry it, and a bad one is refused.
func TestStaleDraftsAreDroppedAndBadOnesRefused(t *testing.T) {
	r := newDraftRig(t)
	watching := r.stream(ownerCaller, r.room, r.child)
	if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: "turn_old", Draft: &contract.Draft{Message: 1, Kind: contract.DraftReply, Text: &contract.DraftEdit{Text: "old"}}}); e != nil {
		t.Fatal(e)
	}
	noDraft(t, watching, "a stream after a stale turn's draft")
	for _, bad := range []contract.Draft{
		{Message: 0, Kind: contract.DraftReply},
		{Message: 1, Kind: "poem"},
		{Message: 1, Kind: contract.DraftReply, Text: &contract.DraftEdit{From: -1}},
		{Message: 1, Kind: contract.DraftReply, Text: &contract.DraftEdit{Text: string(bytes.Repeat([]byte("x"), 64<<10+1))}},
		{Message: 1, Kind: contract.DraftReply, Reasoning: &contract.DraftEdit{Text: "\xff"}},
	} {
		if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: r.turn, Draft: &bad}); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("draft %+v: %+v", bad, e)
		}
	}
	// A label beside a draft still lists, and the draft still streams.
	if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: r.turn, Kind: contract.ActivityModelWait, Label: "waiting for model", Draft: &contract.Draft{Message: 1, Kind: contract.DraftReply, Text: &contract.DraftEdit{Text: "hi "}}}); e != nil {
		t.Fatal(e)
	}
	if got := nextDraft(t, watching); got.Draft.Text.Text != "hi " {
		t.Fatalf("got %+v", got)
	}
	if activity, _ := r.s.live.Activity(r.child); activity.Label != "waiting for model" {
		t.Fatalf("activity %+v", activity)
	}
}
