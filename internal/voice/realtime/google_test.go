package realtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func kinds(evs []Event) []Kind {
	var out []Kind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// A Live model turn becomes one response: it starts with the first audio or
// transcript, and the turn's end closes it with the whole transcript.
func TestGoogleMapsALiveTurnToOneResponse(t *testing.T) {
	g := &Google{}
	g.SessionUpdate(nil)
	step := func(in string, want ...Kind) []Event {
		t.Helper()
		evs, err := g.DecodeAll([]byte(in))
		if err != nil || !reflect.DeepEqual(kinds(evs), want) {
			t.Fatalf("%s: %v, %v; want %v", in, kinds(evs), err, want)
		}
		return evs
	}
	step(`{"setupComplete":{}}`, SessionReady)
	step(`{"serverContent":{"inputTranscription":{"text":"Is CI "}}}`, OwnerPartial)
	evs := step(`{"serverContent":{"inputTranscription":{"text":"green?"},"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQI="}}]}}}`, ResponseStarted, AudioDelta, OwnerPartial, OwnerTranscript)
	if evs[3].Text != "Is CI green?" || evs[1].ItemID != evs[0].ResponseID || string(evs[1].Audio) != "\x01\x02" {
		t.Fatalf("events %+v", evs)
	}
	step(`{"serverContent":{"outputTranscription":{"text":"Yes, "}}}`, VoiceDelta)
	step(`{"serverContent":{"outputTranscription":{"text":"it is."}}}`, VoiceDelta)
	evs = step(`{"serverContent":{"turnComplete":true}}`, VoiceTranscript, AudioDone, ResponseDone)
	if evs[0].Text != "Yes, it is." || evs[2].Text != "completed" {
		t.Fatalf("events %+v", evs)
	}
	step(`{"toolCall":{"functionCalls":[{"id":"c1","name":"check_agents","args":{}}]}}`, ResponseStarted, ToolCall, AudioDone, ResponseDone)
	step(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"data":"AQI="}}]}}}`, ResponseStarted, AudioDelta)
	evs = step(`{"serverContent":{"interrupted":true,"turnComplete":true}}`, Interrupted, AudioDone, ResponseDone)
	if evs[2].Text != "cancelled" {
		t.Fatalf("an interrupted turn ends %q", evs[2].Text)
	}
	step(`{"error":{"message":"blocked by safety","status":"INVALID_ARGUMENT"}}`, Error)
}

// A tool response resumes generation by itself, so the reply after it sends
// nothing; any other reply completes the turn.
func TestGoogleWritesToolResponsesAndTurns(t *testing.T) {
	g := &Google{}
	g.SessionUpdate(nil)
	if _, err := g.DecodeAll([]byte(`{"toolCall":{"functionCalls":[{"id":"c1","name":"check_agents"}]}}`)); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(g.FunctionOutput("c1", `{"result":"Two agents work."}`))
	if want := `{"toolResponse":{"functionResponses":[{"id":"c1","name":"check_agents","response":{"result":"Two agents work."}}]}}`; string(out) != want {
		t.Fatalf("tool response %s, want %s", out, want)
	}
	if got := g.ResponseCreate(""); got != nil {
		t.Fatalf("the reply after a tool response sent %v", got)
	}
	if got, _ := json.Marshal(g.ResponseCreate("m1")); string(got) != `{"clientContent":{"turnComplete":true}}` {
		t.Fatalf("a reply sent %s", got)
	}
	if g.Truncate("i1", 0) != nil {
		t.Fatal("Live has no truncate")
	}
}

func TestGoogleLiveSignsInWithItsKeyAndSetsTheModel(t *testing.T) {
	p, err := GoogleLive("", "", func() string { return "AIza-test" })
	if err != nil {
		t.Fatal(err)
	}
	url, _, err := p.Endpoint(context.Background())
	if err != nil || !strings.HasPrefix(url, GoogleLiveURL+"?") || !strings.Contains(url, "key=AIza-test") {
		t.Fatalf("url %q, %v", url, err)
	}
	if s := p.Session(testSession).(msg); s["model"] != "models/"+GoogleLiveModel || len(s["tools"].([]msg)) != 1 {
		t.Fatalf("session %v", s)
	}
	none, _ := GoogleLive("", "", func() string { return "" })
	if _, _, err := none.Endpoint(context.Background()); err == nil || !strings.Contains(err.Error(), "GOOGLE_API_KEY") {
		t.Fatalf("no key: %v", err)
	}
	if !p.ContentRefused("", "Response blocked by safety settings") {
		t.Fatal("a safety block is not a refusal")
	}
}

// A call on Google Live speaks a Dyna item as a context turn plus a turn
// end, plays the made-up response, and after a tool sends only the tool
// response: Live resumes by itself.
func TestCallSpeaksOverGoogleLive(t *testing.T) {
	f := newFakeServer(t)
	p, err := GoogleLive("", f.url, func() string { return "k" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"setupComplete":{}}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if m := <-f.got; m["setup"] == nil {
		t.Fatalf("first message %v, want the setup", m)
	}
	cf := runCallOn(t, f, conn, []Tool{{Name: "check_agents", Run: func(context.Context, json.RawMessage) (string, error) { return "Two agents work.", nil }}})
	cf.call.Speak("m1", "", "CI is green.")
	if m := <-f.got; field(m, "clientContent", "turnComplete") != false {
		t.Fatalf("got %v, want the item as context", m)
	}
	if m := <-f.got; field(m, "clientContent", "turnComplete") != true {
		t.Fatalf("got %v, want the turn end", m)
	}
	cf.f.reply <- `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"data":"AQI="}}]}}}`
	e := cf.pageGets(t, "audio")
	cf.call.FromPage(PageMessage{Type: "playback", ID: e.ID, State: "started"})
	cf.f.reply <- `{"serverContent":{"turnComplete":true}}`
	cf.pageGets(t, "audio_done")
	cf.call.FromPage(PageMessage{Type: "playback", ID: e.ID, State: "ended", PlayedMs: 0})
	cf.f.reply <- `{"toolCall":{"functionCalls":[{"id":"c1","name":"check_agents","args":{}}]}}`
	if m := <-f.got; field(m, "toolResponse", "functionResponses") == nil {
		t.Fatalf("got %v, want the tool response", m)
	}
	// The call's loop works in order: a marker frame sent now reaches the
	// server right after anything the tool response caused.
	cf.call.FromPage(PageMessage{Type: "audio", Audio: make([]byte, 6)}) // three samples at 24 kHz, two at 16 kHz
	select {
	case m := <-f.got:
		if field(m, "realtimeInput", "audio") == nil {
			t.Fatalf("got %v after the tool response, want nothing before the marker", m)
		}
	case <-time.After(waitLimit):
		t.Fatal("the marker never reached the server")
	}
}

// Live sends the owner's transcript on its own schedule, with no boundary
// of its own. Every late word joins the line it belongs to; a pause of
// InputGap starts a new line, and the model's answer makes a line final.
func TestGoogleKeepsLateWordsTogetherAndSplitsLinesOnAPause(t *testing.T) {
	now := time.Unix(1000, 0)
	g := &Google{now: func() time.Time { return now }}
	g.SessionUpdate(nil)
	decode := func(in string) []Event {
		t.Helper()
		evs, err := g.DecodeAll([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	decode(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"data":"AQI="}}]}}}`)
	decode(`{"serverContent":{"turnComplete":true}}`)
	evs := decode(`{"serverContent":{"inputTranscription":{"text":"Is CI "}}}`) // late, after the turn
	now = now.Add(200 * time.Millisecond)
	evs = append(evs, decode(`{"serverContent":{"inputTranscription":{"text":"green?"}}}`)...)
	if !reflect.DeepEqual(kinds(evs), []Kind{OwnerPartial, OwnerPartial}) || evs[1].Text != "Is CI green?" {
		t.Fatalf("late words %+v, want one growing line", evs)
	}
	now = now.Add(InputGap)
	evs = decode(`{"serverContent":{"inputTranscription":{"text":"Next question."}}}`)
	if !reflect.DeepEqual(kinds(evs), []Kind{OwnerTranscript, OwnerPartial}) || evs[0].Text != "Is CI green?" || evs[1].Text != "Next question." {
		t.Fatalf("after a pause %+v, want the late line final and a new line", evs)
	}
	evs = decode(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"data":"AQI="}}]}}}`)
	if last := evs[len(evs)-1]; last.Kind != OwnerTranscript || last.Text != "Next question." {
		t.Fatalf("the model's answer %+v, want the new line final", evs)
	}
	decode(`{"serverContent":{"turnComplete":true}}`)
	decode(`{"serverContent":{"inputTranscription":{"text":"Thanks."}}}`)
	if got := g.Pending(); got != "Thanks." {
		t.Fatalf("pending %q, want the words not yet final", got)
	}
}

// A call that ends records the owner's words its provider still held.
func TestCallRecordsTheOwnersLastWordsWhenItEnds(t *testing.T) {
	f := newFakeServer(t)
	p, err := GoogleLive("", f.url, func() string { return "k" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"setupComplete":{}}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // setup
	cf := runCallOn(t, f, conn, nil)
	f.reply <- `{"serverContent":{"inputTranscription":{"text":"Goodbye."}}}`
	cf.pageGets(t, "transcript")
	cf.call.FromPage(PageMessage{Type: "hangup"})
	select {
	case got := <-cf.lines:
		if !got.owner || got.text != "Goodbye." {
			t.Fatalf("line %+v, want the owner's last words", got)
		}
	case <-time.After(waitLimit):
		t.Fatal("the owner's last words were lost")
	}
}

// Live never says when the owner speaks, so a mute or a push-to-talk release
// after audio ends the owner's stream; one with no audio before sends nothing.
func TestCallEndsGoogleStreamOnMute(t *testing.T) {
	f := newFakeServer(t)
	p, err := GoogleLive("", f.url, func() string { return "k" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"setupComplete":{}}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // setup
	cf := runCallOn(t, f, conn, nil)
	next := func() map[string]any {
		t.Helper()
		select {
		case m := <-f.got:
			return m
		case <-time.After(waitLimit):
			t.Fatal("the server got nothing")
			return nil
		}
	}
	audio := PageMessage{Type: "audio", Audio: make([]byte, 6)}
	for _, release := range []string{"mute", "push-to-talk release"} {
		cf.call.FromPage(PageMessage{Type: "mute", Muted: false})
		cf.call.FromPage(audio)
		if m := next(); field(m, "realtimeInput", "audio") == nil {
			t.Fatalf("%s: got %v, want the audio", release, m)
		}
		cf.call.FromPage(PageMessage{Type: "mute", Muted: true}) // the page sends mute for both
		if m := next(); field(m, "realtimeInput", "audioStreamEnd") != true {
			t.Fatalf("%s: got %v, want the stream's end", release, m)
		}
	}
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true}) // no audio since: nothing
	cf.call.FromPage(audio)
	if m := next(); field(m, "realtimeInput", "audio") == nil {
		t.Fatalf("got %v, want only the audio after a mute with nothing to end", m)
	}
}

// Words the provider still held when the connection dropped are recorded
// before the redial starts its state afresh: once, and not again at hangup.
func TestCallRecordsHeldWordsBeforeARedial(t *testing.T) {
	f := newFakeServer(t)
	p, err := GoogleLive("", f.url, func() string { return "k" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"setupComplete":{}}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // setup
	<-f.header
	cf := runCallOn(t, f, conn, nil)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, p, testSession) })
	f.reply <- `{"serverContent":{"inputTranscription":{"text":"Is CI "}}}`
	f.reply <- `{"serverContent":{"inputTranscription":{"text":"green?"}}}`
	cf.pageGets(t, "transcript")
	cf.pageGets(t, "transcript")
	f.closeAll()
	cf.pageState(t, "reconnecting")
	cf.timer(t, isBackoff)()
	<-f.header
	<-f.got // setup again
	f.reply <- `{"setupComplete":{}}`
	cf.pageState(t, "listening")
	cf.call.FromPage(PageMessage{Type: "hangup"})
	<-cf.ended // Run has returned: every line it records is in
	cf.ended <- nil
	var lines []line
	for len(cf.lines) > 0 {
		lines = append(lines, <-cf.lines)
	}
	if len(lines) != 1 || !lines[0].owner || lines[0].text != "Is CI green?" {
		t.Fatalf("lines %+v, want the held words once", lines)
	}
}
