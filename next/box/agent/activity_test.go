package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
)

func TestActivityCoalesces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	got := make(chan contract.TurnActivityRequest)
	a := newActivity()
	a.set("turn_1", contract.ActivityTool, "first")
	a.set("turn_1", contract.ActivityTool, "second")
	secret := "sk-" + strings.Repeat("a", 40)
	a.set("turn_1", contract.ActivityTool, strings.Repeat("x", 60)+" "+secret)
	go a.run(ctx, ticks, func(_ context.Context, req contract.TurnActivityRequest) error {
		got <- req
		return errors.New("disconnected")
	})
	for range 2 {
		ticks <- time.Time{}
		if req := <-got; req.Turn != "turn_1" || req.Label != strings.Repeat("x", 60)+" [masked]" {
			t.Fatalf("got %+v", req)
		}
	}
}

// sender records each request and answers with what fail says.
type sender struct {
	got  chan contract.TurnActivityRequest
	fail bool
}

func (s *sender) send(_ context.Context, req contract.TurnActivityRequest) error {
	s.got <- req
	if s.fail {
		return errors.New("disconnected")
	}
	return nil
}

func runActivity(t *testing.T, a *activity) (chan time.Time, *sender) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ticks := make(chan time.Time)
	s := &sender{got: make(chan contract.TurnActivityRequest, 16)}
	go a.run(ctx, ticks, s.send)
	return ticks, s
}

func next(t *testing.T, s *sender) contract.TurnActivityRequest {
	t.Helper()
	select {
	case req := <-s.got:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("no request")
		return contract.TurnActivityRequest{}
	}
}

func quiet(t *testing.T, s *sender) {
	t.Helper()
	select {
	case req := <-s.got:
		t.Fatalf("unexpected request %+v", req)
	case <-time.After(50 * time.Millisecond):
	}
}

var started = time.UnixMilli(1_800_000_000_000).UTC()

// Deltas between ticks go as one request, and a reply of thousands of deltas is tens of requests, not thousands.
func TestDraftDeltasAreBatchedPerTick(t *testing.T) {
	a := newActivity()
	ticks, s := runActivity(t, a)
	a.output("turn_1", pig.Draft{Kind: contract.DraftReply, Started: started})
	for _, word := range []string{"The ", "answer ", "is ", "for", "ty-two."} {
		a.output("turn_1", pig.Draft{Text: word})
	}
	quiet(t, s)
	ticks <- time.Time{}
	req := next(t, s)
	want := &contract.Draft{Message: 1, Kind: contract.DraftReply, Started: started, Text: &contract.DraftEdit{From: 0, Text: "The answer is "}}
	if req.Turn != "turn_1" || req.Kind != "" || req.Label != "" || !reflect.DeepEqual(req.Draft, want) {
		t.Fatalf("got %+v %+v", req, req.Draft)
	}
	// Nothing new streamed, so a tick sends nothing.
	ticks <- time.Time{}
	quiet(t, s)
	a.output("turn_1", pig.Draft{Text: " Done.\n"})
	ticks <- time.Time{}
	if req := next(t, s); !reflect.DeepEqual(req.Draft.Text, &contract.DraftEdit{From: 14, Text: "forty-two. Done.\n"}) || req.Draft.Reasoning != nil {
		t.Fatalf("got %+v", req.Draft.Text)
	}
	// 2 KB of output goes before the tick.
	a.output("turn_1", pig.Draft{Reasoning: strings.Repeat("think ", 400)})
	if req := next(t, s); req.Draft.Reasoning == nil || len(req.Draft.Reasoning.Text) != 2400 || req.Draft.Text != nil {
		t.Fatalf("got %+v", req.Draft)
	}
	sent := 0
	for range 5000 {
		a.output("turn_1", pig.Draft{Text: "word "})
	}
	ticks <- time.Time{}
	for len(s.got) > 0 || sent == 0 {
		next(t, s)
		sent++
	}
	if sent > 20 {
		t.Fatalf("%d requests for 5000 deltas", sent)
	}
}

// A label goes at most once a second, while the draft keeps going on each tick.
func TestLabelsGoAtMostOnceASecondBesideTheDraft(t *testing.T) {
	a := newActivity()
	ticks, s := runActivity(t, a)
	a.set("turn_1", contract.ActivityTool, "bash ls")
	ticks <- time.Time{}
	if req := next(t, s); req.Label != "bash ls" || req.Draft != nil {
		t.Fatalf("got %+v", req)
	}
	a.set("turn_1", contract.ActivityModelWait, "waiting for model")
	a.output("turn_1", pig.Draft{Kind: contract.DraftReply, Started: started})
	a.output("turn_1", pig.Draft{Text: "hello "})
	ticks <- time.Time{}
	if req := next(t, s); req.Label != "" || req.Kind != "" || req.Draft == nil || req.Draft.Text.Text != "hello " {
		t.Fatalf("got %+v", req)
	}
	time.Sleep(labelEvery)
	ticks <- time.Time{}
	if req := next(t, s); req.Label != "waiting for model" || req.Kind != contract.ActivityModelWait || req.Draft != nil {
		t.Fatalf("got %+v", req)
	}
}

// The draft is masked as the transcript is, and an unfinished word waits, so part of a secret never shows.
func TestDraftIsMaskedAndHoldsBackAnUnfinishedWord(t *testing.T) {
	a := newActivity()
	ticks, s := runActivity(t, a)
	secret := "sk-" + strings.Repeat("a", 40)
	a.output("turn_1", pig.Draft{Kind: contract.DraftReply, Started: started})
	a.output("turn_1", pig.Draft{Text: "the key is " + secret[:12]})
	ticks <- time.Time{}
	if req := next(t, s); req.Draft.Text.Text != "the key is " {
		t.Fatalf("got %+v", req.Draft.Text)
	}
	a.output("turn_1", pig.Draft{Text: secret[12:] + " ok"})
	ticks <- time.Time{}
	if req := next(t, s); !reflect.DeepEqual(req.Draft.Text, &contract.DraftEdit{From: 11, Text: "[masked] "}) {
		t.Fatalf("got %+v", req.Draft.Text)
	}
}

// An edit replaces from a UTF-16 offset, as a browser counts, and a mask that changes sent text edits from where it differs.
func TestDraftEditsCountUTF16AndReplaceChangedText(t *testing.T) {
	p := part{raw: "héllo 👋 there ", sent: ""}
	e := p.edit()
	p.sent = e.shown
	p.raw += "TOKEN=abc "
	if e := p.edit(); !reflect.DeepEqual(e.edit, &contract.DraftEdit{From: 15, Text: "TOKEN=abc "}) {
		t.Fatalf("got %+v", e.edit)
	}
	p = part{raw: "x " + strings.Repeat("y", draftHold+1)}
	if e := p.edit(); e.shown != p.raw {
		t.Fatalf("a word longer than the hold waits: %q", e.shown)
	}
	p = part{raw: "ab ", sent: "a😀 "}
	if e := p.edit(); !reflect.DeepEqual(e.edit, &contract.DraftEdit{From: 1, Text: "b "}) {
		t.Fatalf("got %+v", e.edit)
	}
}

// Each model call is a new draft: the message count grows within a turn and starts again with the next turn. A failed
// send is tried again on the next tick and does not loop on bursts.
func TestEachCallIsANewDraftAndFailedSendsWaitForTheTick(t *testing.T) {
	a := newActivity()
	ticks, s := runActivity(t, a)
	a.output("turn_1", pig.Draft{Kind: contract.DraftReply, Started: started})
	a.output("turn_1", pig.Draft{Text: "one "})
	a.output("turn_1", pig.Draft{Kind: contract.DraftCompaction, Started: started.Add(time.Second)})
	ticks <- time.Time{}
	if req := next(t, s); req.Draft.Message != 2 || req.Draft.Kind != contract.DraftCompaction || req.Draft.Text != nil || !req.Draft.Started.Equal(started.Add(time.Second)) {
		t.Fatalf("got %+v", req.Draft)
	}
	s.fail = true
	a.output("turn_2", pig.Draft{Kind: contract.DraftReply, Started: started})
	a.output("turn_2", pig.Draft{Text: strings.Repeat("burst ", 400)})
	req := next(t, s)
	for range 10 {
		a.output("turn_2", pig.Draft{Text: strings.Repeat("more ", 500)})
	}
	quiet(t, s)
	s.fail = false
	ticks <- time.Time{}
	if again := next(t, s); req.Turn != "turn_2" || again.Draft.Message != 1 || again.Draft.Text.From != 0 || len(again.Draft.Text.Text) != 2400+10*2500 {
		t.Fatalf("got %+v then %+v", req.Draft, again.Draft)
	}
}
