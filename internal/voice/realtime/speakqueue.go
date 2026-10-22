package realtime

// Ported from Dyna's web/src/lib/voice/speakQueue.js at ab0800fb (#1090, closed
// when the queue moved into the engine), with batching and replacement from
// qwen-audio-agent server/src/voice/announcement/announcement-manager.mjs and
// progress-announcement-manager.mjs.

import (
	"strings"
	"time"
	"unicode/utf8"
)

// SpeakWait is how long a sent batch may wait, while Voice is silent, for a
// reply to start playing; SpeakTries is how often it is sent.
const (
	SpeakWait  = 15 * time.Second
	SpeakTries = 3
)

// MaxBatchItems and MaxBatchChars bound one batch, as qwen bounds its
// announcement batches.
const (
	MaxBatchItems = 4
	MaxBatchChars = 2000
)

// batchHeader leads a batch of more than one item, and foldHeader a batch
// sent right after Voice stopped mid-reply for it.
const (
	batchHeader = "Several updates for the owner. Tell them together in one short reply, the most important first:"
	foldHeader  = "You stopped mid-reply because new information came in. Say the rest of what you were saying, briefly, and fold in this:"
)

// speakQueue says what Voice must say for Dyna, oldest first. Every item
// waiting when Voice is free goes out in one batch, one reply. A batch
// counts as spoken only when a reply that answers it starts to play; the
// caller names the batch a reply answers, so an unrelated reply or a late
// reply to an earlier try never counts. If no reply starts within
// SpeakWait of Voice falling silent, the batch is sent again, up to
// SpeakTries times, then given up. An item is never said twice. A waiting
// item with a topic is replaced by a newer item with the same topic, as a
// newer progress update replaces an older one.
//
// It is not safe for concurrent use; the call's loop owns it.
type speakQueue struct {
	send     func(key, text string)
	ready    func() bool
	after    func(d time.Duration, fn func()) (cancel func())
	onGiveUp func(key string)

	queue   []*speakItem    // still to say, oldest first
	sent    []*speakItem    // the front of queue that the last send carried
	sentKey string          // the key the last send's reply carries
	seen    map[string]bool // every key ever added
	playing map[string]bool // replies playing now
	flight  *flight         // the batch sent, waiting for its reply
	fold    bool            // the next batch follows a reply Voice cut short
}

type speakItem struct {
	key, topic, text string
	tries            int
}

type flight struct {
	cancel func() // the running wait, or nil while Voice speaks
}

func newSpeakQueue(send func(key, text string), ready func() bool, after func(time.Duration, func()) func(), onGiveUp func(string)) *speakQueue {
	return &speakQueue{send: send, ready: ready, after: after, onGiveUp: onGiveUp, seen: map[string]bool{}, playing: map[string]bool{}}
}

// add queues an item. A waiting item with the same topic that no send
// carries yet takes the new key and text instead.
func (q *speakQueue) add(key, topic, text string) {
	if q.seen[key] {
		return
	}
	q.seen[key] = true
	if topic != "" {
		for _, item := range q.queue[len(q.sent):] {
			if item.topic == topic {
				item.key, item.text = key, text
				q.pump()
				return
			}
		}
	}
	q.queue = append(q.queue, &speakItem{key: key, topic: topic, text: text})
	q.pump()
}

// waiting reports whether an item waits that no send carries yet.
func (q *speakQueue) waiting() bool { return len(q.queue) > len(q.sent) }

func (q *speakQueue) pump() {
	if q.flight != nil || len(q.queue) == 0 || !q.ready() {
		return
	}
	// A resend carries exactly the items of the send before it, under the
	// same key, so a late reply to either counts for the same items. Items
	// added meanwhile wait for the next batch.
	if len(q.sent) == 0 {
		q.sent = q.batch()
	}
	for _, item := range q.sent {
		item.tries++
	}
	texts := make([]string, len(q.sent))
	for i, item := range q.sent {
		texts[i] = item.text
	}
	text := strings.Join(texts, "\n\n")
	switch {
	case q.fold:
		text = foldHeader + "\n\n" + text
	case len(q.sent) > 1:
		text = batchHeader + "\n\n" + text
	}
	q.fold = false
	q.flight = &flight{}
	q.sentKey = q.sent[0].key
	q.send(q.sentKey, text)
	q.arm()
}

// batch is the front of the queue that fits one reply: at least one item.
func (q *speakQueue) batch() []*speakItem {
	n, chars := 0, 0
	for _, item := range q.queue {
		chars += utf8.RuneCountInString(item.text)
		if n > 0 && (n >= MaxBatchItems || chars > MaxBatchChars) {
			break
		}
		n++
	}
	return q.queue[:n]
}

// arm starts the wait, but only while Voice is silent: a reply already
// playing must end before the batch's reply can start.
func (q *speakQueue) arm() {
	f := q.flight
	if f == nil || f.cancel != nil || len(q.playing) > 0 {
		return
	}
	var cancel func()
	fired := false
	cancel = q.after(SpeakWait, func() {
		// A wait that was paused or replaced, even one already firing, does nothing.
		if fired || q.flight != f {
			return
		}
		fired = true
		q.flight = nil
		// A batch sent SpeakTries times is given up; otherwise it goes
		// again as it was. A late reply to it still counts.
		if q.sent[0].tries >= SpeakTries {
			for _, item := range q.sent {
				q.onGiveUp(item.key)
			}
			q.queue, q.sent = q.queue[len(q.sent):], nil
		}
		q.pump()
	})
	f.cancel = func() { fired = true; cancel() }
}

// started notes that reply id began to play. key names the batch the reply
// answers, or is empty for any other reply. A reply to the last batch sent
// counts even when its wait already fired and a resend waits for the call
// to be ready: the batch is spoken, and is never sent again.
func (q *speakQueue) started(id, key string) {
	q.playing[id] = true
	// Any playback pauses the wait; a fresh one starts once all playback
	// has ended, so the deadline never runs while Voice speaks.
	if f := q.flight; f != nil && f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if key == "" || len(q.sent) == 0 || key != q.sentKey {
		return
	}
	q.flight = nil
	q.queue, q.sent = q.queue[len(q.sent):], nil
	q.pump()
}

// lost forgets the send in flight: the provider that took it dropped the
// connection, so no reply to it will come. Its batch goes again.
func (q *speakQueue) lost() {
	if q.flight != nil && q.flight.cancel != nil {
		q.flight.cancel()
	}
	// The batch goes again as it was, so its items share one count of
	// tries, and items added meanwhile wait for the next batch with all
	// their tries.
	q.flight = nil
}

// refused gives up the batch the provider was sent and refused: its items
// are reported unspoken and never sent again. Items that wait stay.
func (q *speakQueue) refused() {
	if q.flight != nil && q.flight.cancel != nil {
		q.flight.cancel()
	}
	q.flight = nil
	for _, item := range q.sent {
		q.onGiveUp(item.key)
	}
	q.queue, q.sent = q.queue[len(q.sent):], nil
}

// ended notes that reply id stopped playing.
func (q *speakQueue) ended(id string) {
	delete(q.playing, id)
	q.arm()
}
