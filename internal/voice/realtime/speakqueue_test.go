package realtime

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Ported one to one from web/src/lib/voice/speakQueue.test.js at ab0800fb.

type timer struct {
	d  time.Duration
	fn func()
	on bool
}

type queueFixture struct {
	q      *speakQueue
	sent   []string
	gaveUp []string
	timers []*timer
	ok     bool
}

func setupQueue(ready bool) *queueFixture {
	f := &queueFixture{ok: ready}
	f.q = newSpeakQueue(
		func(_, text string) { f.sent = append(f.sent, text) },
		func() bool { return f.ok },
		func(d time.Duration, fn func()) func() {
			t := &timer{d: d, fn: fn, on: true}
			f.timers = append(f.timers, t)
			return func() { t.on = false }
		},
		func(key string) { f.gaveUp = append(f.gaveUp, key) },
	)
	return f
}

// fire runs the latest wait still running.
func (f *queueFixture) fire(t *testing.T) {
	t.Helper()
	for i := len(f.timers) - 1; i >= 0; i-- {
		if tm := f.timers[i]; tm.on {
			tm.on = false
			tm.fn()
			return
		}
	}
	t.Fatal("no wait is running")
}

func (f *queueFixture) running() int {
	n := 0
	for _, tm := range f.timers {
		if tm.on {
			n++
		}
	}
	return n
}

func count(items []string, s string) int {
	n := 0
	for _, it := range items {
		if it == s {
			n++
		}
	}
	return n
}

func wantSent(t *testing.T, f *queueFixture, want ...string) {
	t.Helper()
	if !slices.Equal(f.sent, want) {
		t.Fatalf("sent %q, want %q", f.sent, want)
	}
}

func TestSpeakQueueSendsOnceAndCountsAnItemSpokenWhenItsReplyPlays(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "CI is green.")
	wantSent(t, f, "CI is green.")
	if f.timers[0].d != SpeakWait {
		t.Fatalf("wait = %v, want %v", f.timers[0].d, SpeakWait)
	}
	f.q.started("r1", "c1/m1")
	if f.timers[0].on {
		t.Fatal("the wait still runs after the reply began to play")
	}
	f.q.ended("r1")
	wantSent(t, f, "CI is green.")
}

// batchOf is the text of one batch of items.
func batchOf(texts ...string) string {
	return batchHeader + "\n\n" + strings.Join(texts, "\n\n")
}

// A resend carries exactly the items of its first send; an item that
// waits meanwhile goes in the next batch. A batch is given up after
// SpeakTries sends.
func TestSpeakQueueResendsUpToTheCapThenGivesUp(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	f.q.add("c1/m2", "", "two")
	f.fire(t)
	f.fire(t)
	wantSent(t, f, "one", "one", "one")
	f.fire(t) // one has gone out three times
	if !slices.Equal(f.gaveUp, []string{"c1/m1"}) {
		t.Fatalf("gave up %q", f.gaveUp)
	}
	wantSent(t, f, "one", "one", "one", "two")
}

// A late reply to the first send counts only for the items that send
// carried, even after a resend: an item added later is still said.
func TestSpeakQueueALateReplyCountsOnlyForItsOwnItems(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	f.fire(t) // resend of one
	f.q.add("c1/m2", "", "two")
	f.q.started("r1", "c1/m1") // the first send's reply plays at last
	wantSent(t, f, "one", "one", "two")
	if len(f.q.queue) != 1 || f.q.queue[0].key != "c1/m2" {
		t.Fatalf("queue %v, want only two", f.q.queue)
	}
}

func TestSpeakQueueNeverSpeaksTwiceAndKeepsOrder(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	f.q.add("c1/m2", "", "two")
	f.q.add("c1/m1", "", "one") // the same post again
	wantSent(t, f, "one")       // one batch in flight at a time
	f.q.started("r1", "c1/m1")
	wantSent(t, f, "one", "two")
	f.q.started("r2", "c1/m2")
	f.q.add("c1/m1", "", "one")
	f.q.add("c1/m2", "", "two")
	wantSent(t, f, "one", "two")
}

// Every item that waits while Voice is busy goes out in one reply, and that
// reply's playing counts for all of them.
func TestSpeakQueueBatchesWhatWaits(t *testing.T) {
	f := setupQueue(false)
	f.q.add("c1/m1", "", "one")
	f.q.add("c1/m2", "", "two")
	f.q.add("c1/m3", "", "three")
	wantSent(t, f)
	f.ok = true
	f.q.pump()
	wantSent(t, f, batchOf("one", "two", "three"))
	f.q.started("r1", "c1/m1")
	if len(f.q.queue) != 0 {
		t.Fatalf("%d items still queued after their batch played", len(f.q.queue))
	}
}

// A batch holds at most MaxBatchItems items and MaxBatchChars characters,
// but always at least one item.
func TestSpeakQueueBoundsABatch(t *testing.T) {
	f := setupQueue(false)
	for i := range MaxBatchItems + 1 {
		f.q.add("m"+strconv.Itoa(i), "", "item")
	}
	if got := len(f.q.batch()); got != MaxBatchItems {
		t.Fatalf("batch of %d, want %d", got, MaxBatchItems)
	}
	g := setupQueue(false)
	g.q.add("big", "", strings.Repeat("x", MaxBatchChars+1))
	g.q.add("small", "", "y")
	if got := len(g.q.batch()); got != 1 {
		t.Fatalf("batch of %d, want only the big item", got)
	}
}

// A newer item with the same topic replaces a waiting one, as a newer
// progress update replaces an older one. An item already sent is not
// replaced.
func TestSpeakQueueReplacesAWaitingItemOfTheSameTopic(t *testing.T) {
	f := setupQueue(true)
	f.q.add("m1", "q/1", "old in flight")
	f.q.add("m2", "q/2", "old waiting")
	f.q.add("m3", "q/2", "new waiting")
	f.q.add("m4", "q/1", "newer for the flight")
	f.q.started("r1", "m1")
	wantSent(t, f, "old in flight", batchOf("new waiting", "newer for the flight"))
}

func TestSpeakQueueWaitsOnlyWhileVoiceIsSilent(t *testing.T) {
	f := setupQueue(true)
	f.q.started("r0", "") // Voice is talking about something else
	f.q.add("c1/m1", "", "one")
	wantSent(t, f, "one")
	if f.running() != 0 {
		t.Fatal("a wait runs while Voice still speaks")
	}
	f.q.ended("r0")
	if f.running() != 1 {
		t.Fatalf("%d waits run after Voice fell silent, want 1", f.running())
	}
	f.fire(t)
	wantSent(t, f, "one", "one")
}

func TestSpeakQueueNeverCountsAnUnrelatedReply(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	f.q.started("r1", "") // an ordinary reply, queued before the item arrived
	f.q.ended("r1")
	f.fire(t)
	wantSent(t, f, "one", "one") // the item is still pending and sent again
	f.q.started("r2", "c1/m1")   // a retry keeps the item: its reply counts
	f.q.ended("r2")
	f.q.add("c1/m2", "", "two")
	wantSent(t, f, "one", "one", "two")
}

func TestSpeakQueueNeverCountsALateReplyToAnItemGivenUp(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	for range SpeakTries {
		f.fire(t)
	}
	if !slices.Equal(f.gaveUp, []string{"c1/m1"}) {
		t.Fatalf("gave up %q", f.gaveUp)
	}
	f.q.add("c1/m2", "", "two")
	f.q.started("r9", "c1/m1") // the reply to an earlier try of m1, late
	f.q.ended("r9")
	f.fire(t)
	if f.sent[len(f.sent)-1] != "two" || count(f.sent, "two") != 2 {
		t.Fatalf("sent %q, want two still pending and sent again", f.sent)
	}
}

func TestSpeakQueueSendsNothingUntilTheCallIsReady(t *testing.T) {
	f := setupQueue(false)
	f.q.add("c1/m1", "", "one")
	wantSent(t, f)
	f.ok = true
	f.q.pump()
	wantSent(t, f, "one")
}

func TestSpeakQueuePausesTheWaitWhileAnUnrelatedReplyPlays(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	first := f.timers[len(f.timers)-1]
	f.q.started("r1", "") // an unrelated reply starts before the deadline
	if first.on {
		t.Fatal("the original deadline still runs")
	}
	first.fn() // even a deadline that was already firing changes nothing
	wantSent(t, f, "one")
	if len(f.gaveUp) != 0 || f.running() != 0 {
		t.Fatalf("gave up %q with %d waits running, want none", f.gaveUp, f.running())
	}
	f.q.ended("r1")
	if f.running() != 1 {
		t.Fatalf("%d waits run once Voice is silent, want 1", f.running())
	}
}

// A batch lost with its connection goes again as it was: an item added
// meanwhile is not pulled into it, and keeps all its tries.
func TestSpeakQueueResendsALostBatchAsItWas(t *testing.T) {
	f := setupQueue(true)
	f.q.add("c1/m1", "", "one")
	f.fire(t) // one, twice
	f.q.add("c1/m2", "", "two")
	f.ok = false
	f.q.lost()
	f.ok = true
	f.q.pump()
	wantSent(t, f, "one", "one", "one")
	f.fire(t) // one has gone out three times
	if !slices.Equal(f.gaveUp, []string{"c1/m1"}) {
		t.Fatalf("gave up %q, want only one", f.gaveUp)
	}
	wantSent(t, f, "one", "one", "one", "two")
}
