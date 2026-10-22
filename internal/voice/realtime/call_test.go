package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type line struct {
	owner bool
	text  string
}

type callFixture struct {
	f      *fakeServer
	call   *Call
	page   chan PageEvent
	closed chan closed
	lines  chan line
	timers chan timed // every wait the call starts; the test fires them
	ended  chan error // Run's result
}

type closed struct {
	code   int
	reason string
}

// attach attaches a page whose events go to events and whose close goes to shut.
func (cf *callFixture) attach(events chan PageEvent, shut chan closed) func() {
	return cf.call.Attach(&Page{
		Send:  func(e PageEvent) { events <- e },
		Close: func(code int, reason string) { shut <- closed{code, reason} },
	})
}

// startCall runs a call against a fake server. The speak queue's waits go to
// timers, to be fired by the test.
func startCall(t *testing.T, tools ...Tool) *callFixture {
	t.Helper()
	f := newFakeServer(t)
	conn, _ := dial(t, f)
	return runCallOn(t, f, conn, tools)
}

// startCallOn runs a call against f as provider p.
func startCallOn(t *testing.T, f *fakeServer, p Provider, tools ...Tool) *callFixture {
	t.Helper()
	f.reply <- `{"type":"session.updated"}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // session.update
	return runCallOn(t, f, conn, tools)
}

func runCallOn(t *testing.T, f *fakeServer, conn *Conn, tools []Tool) *callFixture {
	t.Helper()
	cf := &callFixture{f: f, page: make(chan PageEvent, 64), closed: make(chan closed, 2), lines: make(chan line, 8), timers: make(chan timed, 16), ended: make(chan error, 1)}
	cf.call = NewCall(conn, tools, func(owner bool, text string) { cf.lines <- line{owner, text} })
	cf.call.after = func(d time.Duration, fn func()) func() { cf.timers <- timed{d, fn}; return func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { cf.ended <- cf.call.Run(ctx) }()
	cf.attach(cf.page, cf.closed)
	t.Cleanup(func() {
		cancel()
		select {
		case <-cf.ended:
		case <-time.After(waitLimit):
			t.Error("the call did not end")
		}
	})
	return cf
}

// pageGets returns the page's next event of that type, skipping others.
func (cf *callFixture) pageGets(t *testing.T, typ string) PageEvent {
	t.Helper()
	for {
		select {
		case e := <-cf.page:
			if e.Type == typ {
				return e
			}
		case <-time.After(waitLimit):
			t.Fatalf("the page got no %s", typ)
		}
	}
}

// waitLimit turns a message that never comes into a failure, not a hang.
const waitLimit = 5 * time.Second

// serverGets checks the type of the server's next message and returns it.
func (cf *callFixture) serverGets(t *testing.T, typ string) map[string]any {
	t.Helper()
	var m map[string]any
	select {
	case m = <-cf.f.got:
	case <-time.After(waitLimit):
		t.Fatalf("the server got no %s", typ)
	}
	if m["type"] != typ {
		t.Fatalf("server got %v, want %s", m, typ)
	}
	return m
}

// mark sends one audio frame from the page. The call's loop handles work in
// order, so anything it sent before the frame reaches the server first.
func (cf *callFixture) mark(t *testing.T) {
	t.Helper()
	cf.call.FromPage(PageMessage{Type: "audio", Audio: []byte{0, 0}})
	cf.serverGets(t, "input_audio_buffer.append")
}

// sync waits until the call's loop has handled every server event sent so
// far, and returns the page events up to then.
func (cf *callFixture) sync(t *testing.T) []PageEvent {
	t.Helper()
	cf.f.reply <- `{"type":"conversation.item.input_audio_transcription.delta","delta":"sync"}`
	var seen []PageEvent
	for {
		select {
		case e := <-cf.page:
			if e.Type == "transcript" && e.Text == "sync" {
				return seen
			}
			seen = append(seen, e)
		case <-time.After(waitLimit):
			t.Fatal("the call did not handle the server's events")
		}
	}
}

// noAudio checks that the page got no audio up to now.
func (cf *callFixture) noAudio(t *testing.T) {
	t.Helper()
	for _, e := range cf.sync(t) {
		if e.Type == "audio" || e.Type == "audio_done" {
			t.Fatalf("the page got %s for %s after the owner interrupted it", e.Type, e.ID)
		}
	}
}

// pageState waits for the page to be told state.
func (cf *callFixture) pageState(t *testing.T, state string) {
	t.Helper()
	for e := cf.pageGets(t, "state"); e.State != state; e = cf.pageGets(t, "state") {
	}
}

// voiceAudio has the server send a response's audio item and waits until
// the page has it.
func (cf *callFixture) voiceAudio(t *testing.T, response, item string, pcm []byte) {
	t.Helper()
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"` + response + `","item_id":"` + item + `","delta":"` + base64.StdEncoding.EncodeToString(pcm) + `"}`
	if e := cf.pageGets(t, "audio"); e.ID != item {
		t.Fatalf("page audio for %q, want %q", e.ID, item)
	}
}

func (cf *callFixture) wantSpoken(t *testing.T, key, text string) {
	t.Helper()
	item := cf.serverGets(t, "conversation.item.create")
	content, _ := field(item, "item", "content").([]any)
	if len(content) != 1 || !strings.HasSuffix(field(content[0], "text").(string), text) {
		t.Fatalf("server got item %v, want one saying %q", item, text)
	}
	create := cf.serverGets(t, "response.create")
	if field(create, "response", "metadata", "dyna_speak") != key || field(create, "response", "tool_choice") != "none" {
		t.Fatalf("server got %v, want a tool-free response for %s", create, key)
	}
}

// A Dyna item goes out only when no response is active and the page plays
// nothing: audio that ends by itself first lets it go out plainly.
func TestCallSpeaksADynaItemOnlyWhenNoResponseIsActive(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceAudio(t, "r1", "i1", []byte{1, 0})
	cf.call.Speak("m1", "", "CI is green.")
	cf.mark(t) // nothing went out while r1 is active
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.sync(t)
	cf.mark(t) // nor while the page still has r1's audio
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 0})
	cf.wantSpoken(t, "m1", "CI is green.")

	cf.f.reply <- `{"type":"response.created","response":{"id":"r2","metadata":{"dyna_speak":"m1"}}}`
	cf.voiceAudio(t, "r2", "i2", []byte{1, 0})
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i2", State: "started"})
	cf.f.reply <- `{"type":"response.done","response":{"id":"r2","status":"completed"}}`
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i2", State: "ended", PlayedMs: 0})
	cf.call.Speak("m1", "", "CI is green.") // said already: dropped
	cf.call.Speak("m2", "", "The merge queue is empty.")
	cf.wantSpoken(t, "m2", "The merge queue is empty.")
}

// The provider can finish a reply the page still plays. A new item then
// still steers that audio: it is cut and cleared at the sentence end, and
// the item goes out folded in, not plainly after the old audio.
func TestCallSteersAudioThatPlaysAfterItsResponseEnded(t *testing.T) {
	cf := startCall(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.voiceSays(t, "r1", "i1", "CI is green. The merge queue is next.")
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.sync(t)
	cf.call.Speak("m2", "", "PR 7 failed review.")
	cf.mark(t) // the item waits for the sentence end
	cf.lastTimer(t)()
	if e := cf.pageGets(t, "clear"); e.ID != "i1" {
		t.Fatalf("clear for %q, want i1", e.ID)
	}
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 400})
	if tr := cf.serverGets(t, "conversation.item.truncate"); tr["item_id"] != "i1" {
		t.Fatalf("truncate %v, want i1", tr)
	}
	msg := cf.serverGets(t, "conversation.item.create")
	content, _ := field(msg, "item", "content").([]any)
	if got, _ := field(content[0], "text").(string); !strings.HasPrefix(got, foldHeader) || !strings.HasSuffix(got, "PR 7 failed review.") {
		t.Fatalf("server got %q, want the folded item", got)
	}
}

// timed is one wait the call started: d long, then fn.
type timed struct {
	d  time.Duration
	fn func()
}

// timer returns the next wait whose length matches, skipping others
// unfired.
func (cf *callFixture) timer(t *testing.T, match func(time.Duration) bool) func() {
	t.Helper()
	for {
		select {
		case w := <-cf.timers:
			if match(w.d) {
				return w.fn
			}
		case <-time.After(waitLimit):
			t.Fatal("no matching wait started")
		}
	}
}

func is(d time.Duration) func(time.Duration) bool {
	return func(got time.Duration) bool { return got == d }
}

// isBackoff matches a redial's wait: at most 10 s plus jitter, and not a
// wait of the speak queue or the response slot.
func isBackoff(d time.Duration) bool {
	return d <= 12*time.Second && d != CancelGrace
}

// lastTimer returns the wait started last, after the call handled every
// server event sent so far. Earlier waits are dropped unfired.
func (cf *callFixture) lastTimer(t *testing.T) func() {
	t.Helper()
	cf.sync(t)
	var last func()
	for {
		select {
		case w := <-cf.timers:
			last = w.fn
		default:
			if last == nil {
				t.Fatal("no wait started")
			}
			return last
		}
	}
}

// voiceSays has the server send a reply's words and audio for item, and the
// page start playing it.
func (cf *callFixture) voiceSays(t *testing.T, response, item, words string) {
	t.Helper()
	cf.f.reply <- `{"type":"response.output_audio_transcript.delta","response_id":"` + response + `","item_id":"` + item + `","delta":"` + words + `"}`
	cf.voiceAudio(t, response, item, make([]byte, 48000)) // 1 s at 24 kHz
	cf.call.FromPage(PageMessage{Type: "playback", ID: item, State: "started"})
}

// wantSteered checks that Voice stopped its reply for new items: the server
// gets a cancel, the page a clear, and once the page reports what it played,
// a truncate and one reply that says the rest and folds the item in.
func (cf *callFixture) wantSteered(t *testing.T, response, item, key, text string) {
	t.Helper()
	cf.serverGets(t, "response.cancel")
	if e := cf.pageGets(t, "clear"); e.ID != item {
		t.Fatalf("clear for %q, want %q", e.ID, item)
	}
	cf.call.FromPage(PageMessage{Type: "playback", ID: item, State: "ended", PlayedMs: 400})
	if tr := cf.serverGets(t, "conversation.item.truncate"); tr["item_id"] != item || tr["audio_end_ms"] != float64(400) {
		t.Fatalf("truncate %v, want %s at 400 ms", tr, item)
	}
	cf.f.reply <- `{"type":"response.done","response":{"id":"` + response + `","status":"cancelled"}}`
	msg := cf.serverGets(t, "conversation.item.create")
	content, _ := field(msg, "item", "content").([]any)
	got, _ := field(content[0], "text").(string)
	if !strings.HasPrefix(got, foldHeader) || !strings.HasSuffix(got, text) {
		t.Fatalf("server got %q, want the fold header and %q", got, text)
	}
	if create := cf.serverGets(t, "response.create"); field(create, "response", "metadata", "dyna_speak") != key {
		t.Fatalf("server got %v, want the reply for %s", create, key)
	}
}

// A root post that comes while Voice says an earlier one stops Voice at the
// end of its sentence; the next reply says the rest and folds the post in.
func TestCallSteersMidReplyForANewItem(t *testing.T) {
	cf := startCall(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.voiceSays(t, "r1", "i1", "CI is green. The merge queue is next.")
	cf.call.Speak("m2", "", "PR 7 failed review.")
	cf.mark(t) // Voice finishes its sentence first
	cf.lastTimer(t)()
	cf.wantSteered(t, "r1", "i1", "m2", "PR 7 failed review.")
}

// The owner's own answer is steered too: the rest of it is carried into the
// next reply, so nothing the owner asked for is lost.
func TestCallSteersAnAnswerToTheOwner(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceSays(t, "r1", "i1", "Two plus two is four. Anything else?")
	cf.call.Speak("m1", "", "CI is red.")
	cf.lastTimer(t)()
	cf.wantSteered(t, "r1", "i1", "m1", "CI is red.")
}

// A reply that plays nothing yet, such as a tool call, is never cut: the
// item goes out after it, as a plain reply.
func TestCallNeverCutsAReplyThatPlaysNothing(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.sync(t)
	cf.call.Speak("m1", "", "CI is red.")
	cf.mark(t) // no cancel
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.wantSpoken(t, "m1", "CI is red.")
}

func TestUntilSentenceEnd(t *testing.T) {
	words := "CI is green. The merge queue is next." // 37 runes; the first sentence ends at 12
	for _, c := range []struct {
		heard, want time.Duration
	}{
		{0, 12 * time.Second / 37},                                   // the first sentence ends
		{500 * time.Millisecond, time.Second - 500*time.Millisecond}, // the last one ends
		{time.Second, SteerWait},                                     // no end left yet
	} {
		if got := untilSentenceEnd(words, time.Second, c.heard); got != c.want {
			t.Errorf("heard %v: %v, want %v", c.heard, got, c.want)
		}
	}
	if got := untilSentenceEnd("", time.Second, 0); got != SteerWait {
		t.Errorf("no words: %v, want %v", got, SteerWait)
	}
}

func TestCallAnswersTheOwnersTurnBeforeADynaItem(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u1"}`
	cf.sync(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.mark(t) // the owner speaks
	cf.f.reply <- `{"type":"input_audio_buffer.speech_stopped"}`
	cf.sync(t)
	cf.mark(t) // the owner's turn is not committed yet
	cf.f.reply <- `{"type":"input_audio_buffer.committed","item_id":"u1"}`
	if create := cf.serverGets(t, "response.create"); create["response"] != nil {
		t.Fatalf("server got %v, want a plain response to the owner", create)
	}
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.sync(t)
	cf.mark(t) // the owner's reply is active
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.wantSpoken(t, "m1", "CI is green.")
}

func TestCallBargeInCancelsAndTruncatesToWhatTheOwnerHeard(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800)) // 100 ms at 24 kHz
	cf.voiceAudio(t, "r1", "i2", make([]byte, 4800))
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "started"})
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u1"}`
	cf.serverGets(t, "response.cancel")
	if e := cf.pageGets(t, "clear"); e.ID != "i1" {
		t.Fatalf("clear for %q, want i1", e.ID)
	}
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 40})
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i2", State: "ended", PlayedMs: 0})
	for _, want := range []struct {
		item string
		ms   float64
	}{{"i1", 40}, {"i2", 0}} {
		tr := cf.serverGets(t, "conversation.item.truncate")
		if tr["item_id"] != want.item || tr["audio_end_ms"] != want.ms {
			t.Fatalf("truncate %v, want %s at %v ms", tr, want.item, want.ms)
		}
	}
}

func TestCallKeepsAnItemThePageHeardInFull(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800))
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.sync(t)
	cf.call.FromPage(PageMessage{Type: "cancel"})
	cf.pageGets(t, "clear")
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 100})
	cf.mark(t) // no response.cancel for an ended response, and no truncate
}

// wantOutput checks that the server's next message is call's output.
func (cf *callFixture) wantOutput(t *testing.T, call, text string) {
	t.Helper()
	out := cf.serverGets(t, "conversation.item.create")
	if field(out, "item", "call_id") != call || !strings.Contains(field(out, "item", "output").(string), text) {
		t.Fatalf("server got %v, want the output of %s", out, call)
	}
}

func TestCallAsksForTheToolReplyOnlyAfterItsResponseEnds(t *testing.T) {
	cf := startCall(t, Tool{Name: "agents", Run: func(context.Context, json.RawMessage) (string, error) {
		return "Two agents work.", nil
	}})
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"agents","arguments":"{}"}`
	cf.wantOutput(t, "c1", "Two agents work.")
	cf.mark(t) // r1 is still active
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	if create := cf.serverGets(t, "response.create"); create["response"] != nil {
		t.Fatalf("server got %v, want a plain response.create", create)
	}
}

// The page shows each tool Voice calls as a row: running, then done or failed.
func TestCallTellsThePageAboutEachToolCall(t *testing.T) {
	cf := startCall(t, Tool{Name: "pr_status", Run: func(context.Context, json.RawMessage) (string, error) {
		return "PR 1091 is green.", nil
	}})
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"pr_status","arguments":"{\"number\":1091}"}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c2","name":"nope","arguments":"{}"}`
	want := map[string][]PageEvent{
		"c1": {{Type: "tool", ID: "c1", Name: "pr_status", Text: `{"number":1091}`, State: "running"}, {Type: "tool", ID: "c1", Name: "pr_status", State: "done"}},
		"c2": {{Type: "tool", ID: "c2", Name: "nope", Text: "{}", State: "running"}, {Type: "tool", ID: "c2", Name: "nope", State: "failed"}},
	}
	got := map[string][]PageEvent{}
	for range 4 {
		e := cf.pageGets(t, "tool")
		got[e.ID] = append(got[e.ID], e)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool events %+v, want %+v", got, want)
	}
}

func TestCallAsksForOneReplyOnceEveryToolIsDone(t *testing.T) {
	release := make(chan struct{})
	cf := startCall(t, Tool{Name: "agents", Run: func(context.Context, json.RawMessage) (string, error) {
		return "Two agents work.", nil
	}}, Tool{Name: "merge_queue", Run: func(context.Context, json.RawMessage) (string, error) {
		<-release
		return "The queue is empty.", nil
	}})
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c2","name":"merge_queue","arguments":"{}"}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"agents","arguments":"{}"}`
	cf.wantOutput(t, "c1", "Two agents work.")
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.sync(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.mark(t) // merge_queue still runs
	close(release)
	cf.wantOutput(t, "c2", "The queue is empty.")
	if create := cf.serverGets(t, "response.create"); create["response"] != nil {
		t.Fatalf("server got %v, want one plain response.create", create)
	}
	cf.mark(t) // the reply to the tools is on its way
	cf.f.reply <- `{"type":"response.created","response":{"id":"r2"}}`
	cf.f.reply <- `{"type":"response.done","response":{"id":"r2","status":"completed"}}`
	cf.wantSpoken(t, "m1", "CI is green.")
}

func TestCallDropsLateAudioOfAnInterruptedResponse(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800)) // 100 ms
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u1"}`
	cf.serverGets(t, "response.cancel")
	cf.pageGets(t, "clear")
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"r1","item_id":"i1","delta":"` + base64.StdEncoding.EncodeToString(make([]byte, 4800)) + `"}`
	cf.noAudio(t)
	// The owner heard all the page got, but the provider holds 100 ms more.
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 100})
	if tr := cf.serverGets(t, "conversation.item.truncate"); tr["item_id"] != "i1" || tr["audio_end_ms"] != float64(100) {
		t.Fatalf("truncate %v, want i1 at 100 ms", tr)
	}
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"r1","item_id":"i1","delta":"AAA="}`
	cf.f.reply <- `{"type":"response.output_audio.done","response_id":"r1","item_id":"i1"}`
	cf.noAudio(t)
	cf.mark(t) // the item heard in part is not cut again
}

func TestCallDropsAnItemThePageNeverGot(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800))
	cf.call.FromPage(PageMessage{Type: "cancel"})
	cf.serverGets(t, "response.cancel")
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"r1","item_id":"i2","delta":"AAA="}`
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"r1","item_id":"i2","delta":"AAA="}`
	if tr := cf.serverGets(t, "conversation.item.truncate"); tr["item_id"] != "i2" || tr["audio_end_ms"] != float64(0) {
		t.Fatalf("truncate %v, want i2 at 0 ms", tr)
	}
	cf.noAudio(t)
	cf.mark(t) // i2 is cut once
}

func TestCallCancelsAResponseInterruptedBeforeItStarted(t *testing.T) {
	cf := startCall(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u1"}`
	cf.sync(t)
	cf.mark(t) // nothing to cancel until the response starts
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.serverGets(t, "response.cancel")
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"r1","item_id":"i1","delta":"AAA="}`
	cf.noAudio(t)
}

// A reply can start after its item's wait fired. While that reply is
// active the call is not ready, so no resend goes out; when it plays, the
// item counts as spoken and is never sent again.
func TestCallCountsASlowReplyAndNeverResendsIt(t *testing.T) {
	cf := startCall(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.sync(t)
	cf.timer(t, is(SpeakWait))() // the wait fires before the reply plays
	cf.sync(t)
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800))
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "started"})
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.sync(t)
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 100})
	cf.mark(t) // a resend would reach the server before this frame
}

func TestCallGivesUpAnItemNoReplyTookAndTellsThePage(t *testing.T) {
	cf := startCall(t)
	cf.call.Speak("m1", "", "CI is green.")
	for range SpeakTries {
		cf.wantSpoken(t, "m1", "CI is green.")
		cf.f.reply <- `{"type":"error","error":{"code":"refused","message":"refused"}}`
		cf.pageGets(t, "error")
		cf.timer(t, is(SpeakWait))()
	}
	if e := cf.pageGets(t, "unspoken"); e.Post != "m1" {
		t.Fatalf("unspoken %q, want m1", e.Post)
	}
}

func TestCallSendsTranscripts(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"conversation.item.input_audio_transcription.delta","delta":"fix"}`
	cf.f.reply <- `{"type":"conversation.item.input_audio_transcription.completed","transcript":"fix CI"}`
	cf.f.reply <- `{"type":"response.output_audio_transcript.done","transcript":"On it."}`
	if e := cf.pageGets(t, "transcript"); e.Text != "fix" || e.Final {
		t.Fatalf("partial %+v", e)
	}
	if e := cf.pageGets(t, "transcript"); e.Text != "fix CI" || !e.Final {
		t.Fatalf("final %+v", e)
	}
	for _, want := range []line{{true, "fix CI"}, {false, "On it."}} {
		if got := <-cf.lines; got != want {
			t.Fatalf("line %+v, want %+v", got, want)
		}
	}
}

func TestCallClosesAPageANewerPageTookOver(t *testing.T) {
	cf := startCall(t)
	cf.pageState(t, "listening")
	newer, shut := make(chan PageEvent, 64), make(chan closed, 1)
	cf.attach(newer, shut)
	if got := <-cf.closed; got != (closed{CloseTakenOver, "taken over"}) {
		t.Fatalf("the older page closed with %+v", got)
	}
	if e := <-newer; e.Type != "state" || e.State != "listening" {
		t.Fatalf("the newer page first got %+v, want the call's state", e)
	}
	cf.f.reply <- `{"type":"conversation.item.input_audio_transcription.delta","delta":"fix"}`
	if e := <-newer; e.Text != "fix" {
		t.Fatalf("the newer page got %+v", e)
	}
}

func TestCallMuteEndsTheOwnersTurn(t *testing.T) {
	cf := startCall(t)
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true})
	cf.mark(t) // no turn to end
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u1"}`
	cf.sync(t)
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true})
	cf.serverGets(t, "input_audio_buffer.commit")
}

func TestCallMuteBeforeTheProviderHeardTheSpeechStillEndsTheTurn(t *testing.T) {
	cf := startCall(t)
	// Push to talk released before the socket opened: the buffered speech
	// and the mute arrive before the provider reports speech.
	cf.call.FromPage(PageMessage{Type: "audio", Audio: []byte{1, 0}})
	cf.serverGets(t, "input_audio_buffer.append")
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true})
	cf.mark(t) // nothing heard yet: no empty commit
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u1"}`
	cf.serverGets(t, "input_audio_buffer.commit")
	// Unmuted, speech waits for the provider's own turn end again.
	cf.f.reply <- `{"type":"input_audio_buffer.committed","item_id":"u1"}`
	cf.serverGets(t, "response.create")
	cf.call.FromPage(PageMessage{Type: "mute", Muted: false})
	cf.f.reply <- `{"type":"input_audio_buffer.speech_started","item_id":"u2"}`
	cf.sync(t)
	cf.mark(t)
}

func TestCallHangupEndsTheCallAndClosesThePage(t *testing.T) {
	cf := startCall(t)
	cf.call.FromPage(PageMessage{Type: "hangup"})
	if err := <-cf.ended; err != nil {
		t.Fatalf("Run = %v, want nil after a hangup", err)
	}
	cf.ended <- nil // for the cleanup
	if got := <-cf.closed; got.code != 1000 {
		t.Fatalf("the page closed with %+v, want 1000", got)
	}
	late := make(chan closed, 1)
	cf.attach(make(chan PageEvent, 1), late)
	if got := <-late; got.code != 1000 {
		t.Fatalf("a page attached after the end closed with %+v, want 1000", got)
	}
}

func TestCallFailureClosesThePageWithTheReason(t *testing.T) {
	cf := startCall(t)
	cf.f.closeAll()
	if err := <-cf.ended; err == nil {
		t.Fatal("Run = nil, want the connection's error")
	}
	cf.ended <- nil
	if got := <-cf.closed; got.code != 1011 || got.reason == "" {
		t.Fatalf("the page closed with %+v, want 1011 and a reason", got)
	}
}

func TestCallEndsTheOldPagesPlaybackWhenANewPageTakesOver(t *testing.T) {
	cf := startCall(t)
	now := time.Unix(0, 0)
	cf.call.do(func() { cf.call.now = func() time.Time { return now } })
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.timer(t, is(SpeakWait)) // the wait for m1's reply
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800)) // 100 ms
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "started"})
	cf.call.do(func() { now = now.Add(40 * time.Millisecond) })
	// The page reloads before it reports the end of i1.
	cf.attach(make(chan PageEvent, 64), make(chan closed, 1))
	if got := <-cf.closed; got.code != CloseTakenOver {
		t.Fatalf("the old page closed with %+v", got)
	}
	cf.serverGets(t, "response.cancel")
	if tr := cf.serverGets(t, "conversation.item.truncate"); tr["item_id"] != "i1" || tr["audio_end_ms"] != float64(40) {
		t.Fatalf("truncate %v, want i1 at what played, 40 ms", tr)
	}
	// i1 no longer counts as playing, so the next item's wait runs.
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"cancelled"}}`
	cf.call.Speak("m2", "", "The queue is empty.")
	cf.wantSpoken(t, "m2", "The queue is empty.")
	cf.timer(t, is(SpeakWait)) // m2's wait starts: the old page's playback no longer counts
}

func TestCallCancelsAPendingResponseWhenThePageLeaves(t *testing.T) {
	cf := startCall(t)
	// Swap startCall's page for one that can leave.
	leave := cf.attach(make(chan PageEvent, 64), make(chan closed, 1))
	<-cf.closed
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	wait := cf.timer(t, is(SpeakWait))
	leave() // the page goes before the response starts
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.serverGets(t, "response.cancel")
	cf.f.reply <- `{"type":"response.output_audio.delta","response_id":"r1","item_id":"i1","delta":"AAA="}`
	if tr := cf.serverGets(t, "conversation.item.truncate"); tr["item_id"] != "i1" || tr["audio_end_ms"] != float64(0) {
		t.Fatalf("truncate %v, want i1 at 0 ms", tr)
	}
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"cancelled"}}`
	// With no page, the transcript line shows the call handled response.done.
	cf.f.reply <- `{"type":"response.output_audio_transcript.done","transcript":"sync"}`
	<-cf.lines
	wait()     // m1 was never heard: its wait ends and it may go again
	cf.mark(t) // but no page would hear it: nothing goes out
	again := make(chan PageEvent, 64)
	cf.attach(again, make(chan closed, 1))
	if e := <-again; e.Type != "state" || e.State != "listening" {
		t.Fatalf("the new page first got %+v, want listening", e)
	}
	cf.wantSpoken(t, "m1", "CI is green.")
}

// redialTo makes cf's call redial f's server.
func (cf *callFixture) redialTo(dial func(context.Context) (*Conn, error)) {
	cf.call.do(func() { cf.call.Redial = dial })
}

// A dropped provider connection is dialed again after the backoff; the
// owner's audio meanwhile waits and goes out on the new connection.
func TestCallRedialsADroppedConnectionAndKeepsTheOwnersAudio(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	<-cf.f.header // the first connection
	cf.f.closeAll()
	cf.pageState(t, "reconnecting")
	cf.call.FromPage(PageMessage{Type: "audio", Audio: []byte{1, 2}})
	cf.timer(t, isBackoff)() // the backoff ends
	<-cf.f.header            // the second connection
	if m := <-cf.f.got; m["type"] != "session.update" {
		t.Fatalf("server got %v, want the session again", m)
	}
	cf.f.reply <- `{"type":"session.updated"}`
	if m := cf.serverGets(t, "input_audio_buffer.append"); m["audio"] != "AQI=" {
		t.Fatalf("server got %v, want the audio that waited", m)
	}
	cf.pageState(t, "listening")
	cf.call.Speak("m1", "", "CI is green.") // the call works on the new connection
	cf.wantSpoken(t, "m1", "CI is green.")
}

// A call whose redials keep failing ends after MaxRedials of them.
func TestCallEndsAfterMaxRedialsFail(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(context.Context) (*Conn, error) { return nil, errors.New("no route") })
	cf.f.closeAll()
	for range MaxRedials {
		cf.timer(t, isBackoff)()
	}
	err := <-cf.ended
	if err == nil || !strings.Contains(err.Error(), "lost the voice connection: no route") {
		t.Fatalf("Run = %v, want the redial error", err)
	}
	cf.ended <- nil
}

func TestBackoffDoublesToItsCapWithJitter(t *testing.T) {
	b := newBackoff()
	b.random = func() float64 { return 0.5 } // no jitter
	var got []time.Duration
	for range 7 {
		got = append(got, b.next())
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
	b.reset()
	b.random = func() float64 { return 1 } // the most jitter
	if d := b.next(); d != 600*time.Millisecond {
		t.Fatalf("after reset with full jitter: %v, want 600ms", d)
	}
}

// A response asked for that never starts is cancelled; if the slot is still
// not free CancelGrace later, the connection is dropped and the call redials.
func TestCallRecoversAResponseThatNeverStarts(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.timer(t, is(DefaultResponseStart))()
	cf.serverGets(t, "response.cancel")
	cf.timer(t, is(CancelGrace))()
	cf.pageState(t, "reconnecting")
}

// A response that starts frees the slot's start wait: a wait that fires
// after that does nothing.
func TestCallLeavesAStartedResponseAlone(t *testing.T) {
	cf := startCall(t)
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	start := cf.timer(t, is(DefaultResponseStart))
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`
	cf.sync(t)
	start()
	cf.mark(t) // no cancel went out
}

// A response that sends nothing for ResponseInactivity is cancelled.
func TestCallCancelsAResponseThatGoesQuiet(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.timer(t, is(ResponseInactivity))()
	cf.serverGets(t, "response.cancel")
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"cancelled"}}`
	grace := cf.timer(t, is(CancelGrace))
	cf.sync(t)
	grace() // the slot is free: the connection stays
	cf.mark(t)
}

// A refusal because a response is active means the provider holds the
// slot: the call sends nothing more until it is free, and repeated
// refusals never restart its guard. At the first deadline the call
// cancels, and CancelGrace later it redials.
func TestCallGuardsABusySlotFromItsFirstDeadline(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	busy := `{"type":"error","error":{"code":"conversation_already_has_active_response","message":"busy"}}`
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	start := cf.timer(t, is(DefaultResponseStart)) // the first deadline
	wait := cf.timer(t, is(SpeakWait))
	cf.f.reply <- busy
	cf.pageGets(t, "error")
	wait() // the queue's wait: no resend into a busy slot
	cf.f.reply <- busy
	cf.pageGets(t, "error")
	cf.mark(t) // nothing was sent again
	start()
	cf.serverGets(t, "response.cancel")
	cf.f.reply <- busy // a refusal while cancelling keeps the grace
	cf.pageGets(t, "error")
	cf.timer(t, is(CancelGrace))()
	cf.pageState(t, "reconnecting")
}

// A drop in the middle of Voice's audio clears that audio on the page and
// forgets it, so its late receipts block nothing: a later item that no
// reply takes is still sent again after its wait.
func TestCallClearsAudioOfADroppedConnection(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	<-cf.f.header
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.voiceAudio(t, "r1", "i1", make([]byte, 4800))
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "started"})
	cf.f.closeAll() // no audio_done comes
	if e := cf.pageGets(t, "clear"); e.ID != "i1" {
		t.Fatalf("clear for %q, want i1", e.ID)
	}
	cf.timer(t, isBackoff)() // the backoff ends
	<-cf.f.header
	<-cf.f.got // session.update
	cf.f.reply <- `{"type":"session.updated"}`
	cf.pageState(t, "listening")
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "started"}) // late receipts
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 40})
	cf.call.Speak("m1", "", "CI is green.")
	cf.wantSpoken(t, "m1", "CI is green.")
	cf.f.reply <- `{"type":"error","error":{"code":"refused","message":"refused"}}` // no reply takes it
	cf.pageGets(t, "error")
	cf.timer(t, is(SpeakWait))() // the wait runs: nothing counts as playing
	cf.wantSpoken(t, "m1", "CI is green.")
}

// A tool that finishes after its connection dropped still closes its row on
// the page, but its output never goes to the new connection.
func TestCallClosesAToolRowAcrossAReconnect(t *testing.T) {
	release := make(chan struct{})
	cf := startCall(t, Tool{Name: "agents", Run: func(context.Context, json.RawMessage) (string, error) {
		<-release
		return "Two agents work.", nil
	}})
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	<-cf.f.header
	cf.f.reply <- `{"type":"response.function_call_arguments.done","call_id":"c1","name":"agents","arguments":"{}"}`
	if e := cf.pageGets(t, "tool"); e.State != "running" {
		t.Fatalf("tool %+v, want running", e)
	}
	cf.f.closeAll()
	cf.timer(t, isBackoff)() // the backoff ends
	<-cf.f.header
	<-cf.f.got // session.update
	cf.f.reply <- `{"type":"session.updated"}`
	cf.pageState(t, "listening")
	close(release)
	if e := cf.pageGets(t, "tool"); e.ID != "c1" || e.State != "done" {
		t.Fatalf("tool %+v, want c1 done", e)
	}
	cf.mark(t) // no function output reached the new connection
}

const refusal = `{"type":"error","error":{"code":"content_policy_violation","message":"Your request was rejected by the content filter."}}`

// recoverFrom has the server refuse content, then completes the redial the
// call makes.
func (cf *callFixture) recoverFrom(t *testing.T) {
	t.Helper()
	cf.f.reply <- refusal
	if e := cf.pageGets(t, "error"); e.Message != safetyNotice {
		t.Fatalf("page error %q, want the safety notice", e.Message)
	}
	cf.pageState(t, "reconnecting")
	cf.f.closeAll() // the server's side of the dropped connection takes no reply
	cf.timer(t, isBackoff)()
	<-cf.f.header
	<-cf.f.got // session.update
	cf.f.reply <- `{"type":"session.updated"}`
	cf.pageState(t, "listening")
}

// A content-safety refusal drops the connection: the call redials, starts
// fresh without the refused turn, and Voice says one short line about it.
func TestCallRecoversFromAContentRefusal(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	<-cf.f.header
	cf.recoverFrom(t)
	cf.wantSpoken(t, "safety/1", safetyLine)
}

// Only a completed reply resets the count. After MaxSafetyRecoveries
// refusals in a row, the next one ends the call.
func TestCallStopsAfterRepeatedContentRefusals(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	<-cf.f.header
	cf.recoverFrom(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}` // resets the count
	cf.sync(t)
	for range MaxSafetyRecoveries {
		cf.recoverFrom(t)
	}
	cf.f.reply <- refusal
	if e := cf.pageGets(t, "error"); e.Message != safetyStop {
		t.Fatalf("page error %q, want the stop message", e.Message)
	}
	if err := <-cf.ended; err == nil || !strings.Contains(err.Error(), "refused the conversation") {
		t.Fatalf("Run = %v, want the refusal", err)
	}
	cf.ended <- nil
}

// A refusal while a batch is out gives that batch up: the page hears it was
// not spoken, the new connection never gets it, and Voice says its line.
// An item that waited is said after it.
func TestCallGivesUpABatchTheProviderRefused(t *testing.T) {
	cf := startCall(t)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, testProvider(cf.f), testSession) })
	<-cf.f.header
	cf.call.Speak("m1", "", "Something the provider refuses.")
	cf.wantSpoken(t, "m1", "Something the provider refuses.")
	cf.f.reply <- refusal
	if e := cf.pageGets(t, "unspoken"); e.Post != "m1" {
		t.Fatalf("unspoken %q, want m1", e.Post)
	}
	cf.pageState(t, "reconnecting")
	cf.f.closeAll() // the server's side of the dropped connection takes no reply
	cf.timer(t, isBackoff)()
	<-cf.f.header
	<-cf.f.got // session.update
	cf.f.reply <- `{"type":"session.updated"}`
	cf.pageState(t, "listening")
	cf.wantSpoken(t, "safety/1", safetyLine) // the first item on the new connection
}

// clockAt gives the call a clock the test moves.
func (cf *callFixture) clockAt(start time.Time) *time.Time {
	now := start
	cf.call.do(func() { cf.call.now = func() time.Time { return now } })
	return &now
}

// Progress of work that has run a minute is told once, as one line; a task
// that ends first is never told.
func TestCallTellsProgressOnceAMinute(t *testing.T) {
	cf := startCall(t)
	start := time.Unix(1000, 0)
	now := cf.clockAt(start)
	cf.call.Progress("q1", start, "Rerunning the failed job.")
	cf.call.Progress("q2", start, "Writing the fix.")
	cf.call.EndProgress("q2") // root answered q2
	wait := cf.timer(t, is(ProgressInterval))
	cf.call.do(func() { *now = start.Add(ProgressInterval) })
	wait()
	cf.wantSpoken(t, "progress/q1/1", "Rerunning the failed job.")
	cf.call.Progress("q1", start, "Still rerunning.") // within the minute after a line
	if w := cf.timer(t, func(d time.Duration) bool { return d > 0 }); w == nil {
		t.Fatal("no wait for the next line")
	}
}

// A due line waits while the call cannot speak, and tries again shortly.
func TestCallHoldsProgressWhileVoiceSpeaks(t *testing.T) {
	cf := startCall(t)
	start := time.Unix(1000, 0)
	now := cf.clockAt(start.Add(ProgressInterval))
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.sync(t)
	cf.call.Progress("q1", start, "Rerunning the failed job.")
	quiet := cf.timer(t, is(ProgressQuiet)) // a minute in: due once it is quiet
	cf.call.do(func() { *now = now.Add(ProgressQuiet) })
	quiet() // r1 is active, so it waits
	retry := cf.timer(t, is(ProgressRetry))
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.sync(t)
	cf.call.do(func() { *now = now.Add(ProgressRetry) })
	retry()
	cf.wantSpoken(t, "progress/q1/1", "Rerunning the failed job.")
}

// Once Voice dispatched the owner's question, its answer comes later in the
// call. A check Voice then calls on the same turn is refused without a
// reply, so its empty result never speaks over the answer. The owner's next
// turn may use tools again.
func TestCallRefusesOtherToolsOnceATurnIsDispatched(t *testing.T) {
	checks := 0
	cf := startCall(t, Tool{Name: "dispatch", Later: true, Run: func(context.Context, json.RawMessage) (string, error) {
		return "Started.", nil
	}}, Tool{Name: "recent", Run: func(context.Context, json.RawMessage) (string, error) {
		checks++
		return "Nothing about that.", nil
	}})
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"dispatch","arguments":"{}"}`
	cf.wantOutput(t, "c1", "Started.")
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.serverGets(t, "response.create") // "I've started that"
	cf.f.reply <- `{"type":"response.created","response":{"id":"r2"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r2","call_id":"c2","name":"recent","arguments":"{}"}`
	cf.wantOutput(t, "c2", "already being answered")
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r2","call_id":"c3","name":"dispatch","arguments":"{}"}`
	cf.wantOutput(t, "c3", "already being answered")
	cf.f.reply <- `{"type":"response.done","response":{"id":"r2","status":"completed"}}`
	cf.mark(t) // no reply is asked for the refused calls
	if checks != 0 {
		t.Fatalf("the check ran %d times after the dispatch", checks)
	}
	cf.f.reply <- `{"type":"input_audio_buffer.committed","item_id":"u2"}`
	cf.serverGets(t, "response.create") // the owner's new turn is answered
	cf.f.reply <- `{"type":"response.created","response":{"id":"r3"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r3","call_id":"c4","name":"recent","arguments":"{}"}`
	cf.wantOutput(t, "c4", "Nothing about that.")
}

// A dispatch that fails accepted no work, so no answer is coming: the same
// owner turn may dispatch again, without a new commit.
func TestCallLetsAFailedDispatchBeRetried(t *testing.T) {
	runs := 0
	cf := startCall(t, Tool{Name: "dispatch", Later: true, Run: func(context.Context, json.RawMessage) (string, error) {
		runs++
		if runs == 1 {
			return "", errors.New("the request is empty")
		}
		return "accepted", nil
	}})
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"dispatch","arguments":"{}"}`
	cf.wantOutput(t, "c1", "the request is empty")
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.serverGets(t, "response.create")
	cf.f.reply <- `{"type":"response.created","response":{"id":"r2"}}`
	cf.f.reply <- `{"type":"response.function_call_arguments.done","response_id":"r2","call_id":"c2","name":"dispatch","arguments":"{\"text\":\"x\"}"}`
	cf.wantOutput(t, "c2", "accepted")
	if runs != 2 {
		t.Fatalf("dispatch ran %d times, want the retry to run", runs)
	}
}
