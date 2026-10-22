package realtime

// Ported from qwen-audio-agent server/src/voice/realtime-session-runtime.mjs,
// realtime-response-slot.mjs and realtime-turn-state.mjs.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gorilla/websocket"
)

// PageMessage is one message from the owner's page.
type PageMessage struct {
	Type     string `json:"type"` // audio, mute, cancel, playback or hangup
	Audio    []byte `json:"audio,omitempty"`
	Muted    bool   `json:"muted,omitempty"`
	ID       string `json:"id,omitempty"`        // playback: the audio item
	State    string `json:"state,omitempty"`     // playback: started or ended
	PlayedMs int64  `json:"played_ms,omitempty"` // playback ended: how much the owner heard
}

// PageEvent is one message to the owner's page.
type PageEvent struct {
	Type    string `json:"type"`            // state, audio, audio_done, clear, transcript, unspoken, tool or error
	State   string `json:"state,omitempty"` // tool: running, done or failed
	Name    string `json:"name,omitempty"`  // tool: the tool's name; Text has its arguments
	ID      string `json:"id,omitempty"`
	Audio   []byte `json:"audio,omitempty"`
	Text    string `json:"text,omitempty"`
	Final   bool   `json:"final,omitempty"`
	Post    string `json:"post,omitempty"`
	Message string `json:"message,omitempty"`
}

// Page is the owner's browser on the call: Send delivers one event, and
// Close ends the page's socket with a WebSocket close code and reason.
type Page struct {
	Send  func(PageEvent)
	Close func(code int, reason string)
}

// CloseTakenOver closes a page that a newer page of the same call replaced.
const CloseTakenOver = 4001

// errHangup ends a call the owner hung up.
var errHangup = errors.New("the owner hung up")

// Call runs one conversation on an open Conn. One goroutine, Run's loop,
// owns its state; Speak and FromPage hand work to that loop.
type Call struct {
	// Greeting is queued once, when the first page attaches. Set it before Run.
	Greeting string
	conn     *Conn // nil while the call redials
	provider Provider
	// Redial opens a new connection when the provider drops this one. Nil
	// ends the call on the first drop.
	Redial func(context.Context) (*Conn, error)
	tools  []Tool
	page   *Page // the attached page, or nil
	// transcript receives each finished line: the owner's when owner is true.
	transcript func(owner bool, text string)
	after      func(time.Duration, func()) func()
	now        func() time.Time

	inbox chan func()
	done  chan struct{}
	err   error

	queue      *speakQueue
	active     string // the response in progress
	requested  bool   // a response.create is on its way
	owed       bool   // the owner's turn or tool outputs await a plain response
	tooling    int    // tool runs in progress
	speaking   bool   // the owner speaks, or their turn is not yet committed
	muted      bool   // the page muted the owner: their speech ends the turn at once
	cancelNext bool   // the response on its way is cancelled once it starts
	state      string
	keys       map[string]string    // response -> the Dyna item it speaks
	cancelled  map[string]bool      // responses cancelled: their late audio is dropped
	known      map[string]bool      // every audio item seen
	responses  map[string]string    // audio item -> its response
	audio      map[string]int       // audio item -> PCM bytes sent to the page
	onPage     []string             // audio items sent and not yet ended, oldest first
	cleared    map[string]bool      // audio items the page was told to stop
	playing    map[string]time.Time // audio items the page plays -> when they started

	backoff     *backoff
	connectedAt time.Time
	pending     [][]byte // the owner's audio while the call redials
	redials     int      // failed dials in a row
	gen         int      // counts connections: a tool from an earlier one answers nothing
	slot        slot     // the provider's response slot
	// correction asks Voice to answer again after a reply a guard rejected;
	// correcting is the reply to it, which is never corrected again.
	correction      string
	correctionAsked bool
	correcting      string
	asked           string   // the key of the response asked for last
	streamed        bool     // the owner's audio went out since the last commit
	progress        progress // progress lines of work still running
	silence         func()   // stops the silence a stepped model gets while the owner is muted

	safetyRecoveries int  // content-safety refusals since the last completed reply
	recovering       bool // the call redials after a refusal: Voice says so once it is back
	recoveries       int  // safety recoveries so far: each says its line once
}

// NewCall prepares a conversation on conn with tools. transcript is called
// from the call's loop.
func NewCall(conn *Conn, tools []Tool, transcript func(owner bool, text string)) *Call {
	c := &Call{
		conn: conn, provider: conn.Provider, tools: tools, transcript: transcript,
		now: time.Now,
		after: func(d time.Duration, fn func()) func() {
			t := time.AfterFunc(d, fn)
			return func() { t.Stop() }
		},
		inbox: make(chan func(), 64), done: make(chan struct{}), backoff: newBackoff(),
		keys: map[string]string{}, cancelled: map[string]bool{}, known: map[string]bool{}, responses: map[string]string{}, audio: map[string]int{}, cleared: map[string]bool{}, playing: map[string]time.Time{}, progress: progress{tasks: map[string]*progressTask{}},
	}
	c.queue = newSpeakQueue(c.say, c.ready, c.later, func(key string) { c.toPage(PageEvent{Type: "unspoken", Post: key}) })
	return c
}

// Speak queues an item for Voice to say: text tells the model what to say.
// key names the item, such as its post ID; an item with a key already
// queued or said is dropped. A waiting item with the same topic is
// replaced; an empty topic replaces nothing. Items arriving during speech
// share one pending response after the current playback ends.
func (c *Call) Speak(key, topic, text string) {
	c.do(func() {
		if c.provider.Caps.NoClientResponses {
			c.toPage(PageEvent{Type: "unspoken", Post: key})
			return
		}
		c.queue.add(key, topic, text)
	})
}

// Context appends execution progress without requesting a response. Some
// providers cannot accept text independently from speech; those skip it.
func (c *Call) Context(text string) {
	c.do(func() {
		if p, ok := c.provider.Protocol.(interface{ Context(string) any }); ok {
			c.send(p.Context(text))
		}
	})
}

// FromPage hands the call one message from the owner's page.
func (c *Call) FromPage(m PageMessage) { c.do(func() { c.fromPage(m) }) }

// Attach makes p the call's page and returns leave, which detaches it. A
// page attached before closes as taken over. A call that has ended closes
// p at once. Run must be running: Attach waits for its loop.
func (c *Call) Attach(p *Page) (leave func()) {
	attached := make(chan struct{})
	c.do(func() {
		if c.page != nil {
			c.page.Close(CloseTakenOver, "taken over")
			c.dropPage()
		}
		c.page = p
		p.Send(PageEvent{Type: "state", State: c.state})
		if c.Greeting != "" {
			if !c.provider.Caps.NoClientResponses {
				c.queue.add("greeting", "", c.Greeting)
			}
			c.Greeting = ""
		}
		close(attached)
	})
	select {
	case <-attached:
	case <-c.done:
		select {
		case <-attached: // Run closed it as the call ended
		default:
			p.Close(websocket.CloseNormalClosure, "the call ended")
		}
	}
	return func() {
		c.do(func() {
			if c.page == p {
				c.dropPage()
			}
		})
	}
}

// dropPage detaches the page. No one hears the active or requested
// response any more, so it is cancelled, and each item the page had is ended where the owner
// stopped hearing it: what played since the page reported it started, or
// nothing.
func (c *Call) dropPage() {
	c.page = nil
	c.interrupt()
	for _, id := range slices.Clone(c.onPage) {
		played := time.Duration(0)
		if at, ok := c.playing[id]; ok {
			played = min(c.now().Sub(at), c.length(id))
		}
		c.cleared[id] = true
		c.playbackEnded(id, played)
	}
}

// length is how much audio of item id the page got.
func (c *Call) length(id string) time.Duration {
	return time.Duration(c.audio[id]/2) * time.Second / time.Duration(c.provider.OutputRate)
}

// toPage sends e to the attached page; with none, e is dropped.
func (c *Call) toPage(e PageEvent) {
	if c.page != nil {
		c.page.Send(e)
	}
}

// do runs fn on the call's loop, unless the call has ended.
func (c *Call) do(fn func()) {
	select {
	case c.inbox <- fn:
	case <-c.done:
	}
}

// later is the speak queue's timer: fn runs on the loop.
func (c *Call) later(d time.Duration, fn func()) func() {
	return c.after(d, func() { c.do(fn) })
}

// Run runs the call until ctx ends, the owner hangs up or the connection
// fails. Then it closes the connection and the page, and returns why the
// call failed, or nil.
func (c *Call) Run(ctx context.Context) (err error) {
	defer close(c.done)
	defer c.savePending()
	defer func() {
		if c.conn != nil {
			c.conn.Close()
		}
	}()
	defer func() {
		if errors.Is(err, errHangup) || errors.Is(err, context.Canceled) {
			err = nil
		}
		if c.page == nil {
			return
		}
		if err != nil {
			c.page.Close(websocket.CloseInternalServerErr, err.Error())
		} else {
			c.page.Close(websocket.CloseNormalClosure, "the call ended")
		}
	}()
	c.connectedAt = c.now()
	go c.read(ctx, c.conn)
	c.setState("listening")
	for c.err == nil {
		select {
		case fn := <-c.inbox:
			fn()
			c.next()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.err
}

// read hands the call each event of conn until conn fails. Events of a
// connection the call has replaced are dropped.
func (c *Call) read(ctx context.Context, conn *Conn) {
	for {
		ev, err := conn.Next()
		if err != nil {
			c.do(func() { c.lost(ctx, conn, err) })
			return
		}
		c.do(func() {
			if conn == c.conn {
				c.event(ctx, ev)
			}
		})
	}
}

// lost starts a redial when the provider drops conn. The provider forgets
// the response it was giving, so the call does too, and the page hears the
// call is reconnecting.
func (c *Call) lost(ctx context.Context, conn *Conn, err error) {
	if conn != c.conn {
		return
	}
	if c.Redial == nil {
		c.err = err
		return
	}
	conn.Close()
	c.conn = nil
	c.gen++
	c.savePending()
	if c.now().Sub(c.connectedAt) >= StableConnection {
		c.backoff.reset()
	}
	c.active, c.requested, c.cancelNext, c.owed, c.speaking = "", false, false, false, false
	c.correction, c.correctionAsked = "", false
	c.releaseSlot()
	// The page stops the dropped connection's audio; the call forgets it at
	// once, so a late receipt for it truncates and blocks nothing.
	if len(c.onPage) > 0 {
		c.toPage(PageEvent{Type: "clear", ID: c.onPage[0]})
	}
	for _, id := range c.onPage {
		c.queue.ended(id)
		delete(c.playing, id)
		delete(c.audio, id)
		delete(c.responses, id)
	}
	c.onPage = nil
	c.cleared = map[string]bool{} // the new connection has none of these items
	if c.recovering {
		// What the provider refused must not go to the new connection: the
		// batch it was sent, and audio that waited from before the refusal.
		c.queue.refused()
		c.pending = nil
	} else {
		c.queue.lost()
	}
	c.setState("reconnecting")
	c.redial(ctx)
}

// keepStepping sends a stepped model a second of silence each second while
// the owner is muted, and stops when they unmute.
func (c *Call) keepStepping() {
	if !c.provider.Caps.SteppedByInput {
		return
	}
	if !c.muted {
		if c.silence != nil {
			c.silence()
			c.silence = nil
		}
		return
	}
	if c.silence != nil {
		return
	}
	quiet := make([]byte, 2*c.provider.InputRate)
	var tick func()
	tick = func() {
		if !c.muted || c.silence == nil {
			return
		}
		if c.conn != nil {
			c.send(c.provider.Protocol.AudioAppend(quiet))
		}
		c.silence = c.later(time.Second, tick)
	}
	c.silence = c.later(time.Second, tick)
}

// savePending records the owner's words a provider still holds until a
// boundary, before the call ends or a redial starts its state afresh, so
// they are not lost.
func (c *Call) savePending() {
	if p, ok := c.provider.Protocol.(interface{ Pending() string }); ok {
		if text := p.Pending(); text != "" {
			c.transcript(true, text)
		}
	}
}

// redial dials again after the backoff. A dial that fails MaxRedials times
// in a row ends the call.
func (c *Call) redial(ctx context.Context) {
	c.later(c.backoff.next(), func() {
		go func() {
			conn, err := c.Redial(ctx)
			if err == nil && ctx.Err() != nil {
				conn.Close()
				return
			}
			c.do(func() { c.redialed(ctx, conn, err) })
		}()
	})
}

func (c *Call) redialed(ctx context.Context, conn *Conn, err error) {
	if err != nil {
		if c.redials++; c.redials >= MaxRedials {
			c.err = fmt.Errorf("lost the voice connection: %w", err)
			return
		}
		c.redial(ctx)
		return
	}
	c.redials = 0
	c.conn, c.connectedAt = conn, c.now()
	if p, ok := c.provider.Protocol.(InputMuter); ok && c.muted {
		c.send(p.Mute(true)) // a new connection starts unmuted
	}
	for _, audio := range c.pending {
		c.send(c.provider.Protocol.AudioAppend(audio))
		c.streamed = true
	}
	c.pending = nil
	go c.read(ctx, conn)
	c.setState("listening")
	if c.recovering {
		c.recovering = false
		c.recoveries++
		c.queue.add(fmt.Sprintf("safety/%d", c.recoveries), "", safetyLine)
	}
}

// idle reports whether the call may request a response: the provider is
// connected, a page is there to hear it, none is active or on its way, no
// tool runs, and the owner is not mid-turn.
func (c *Call) idle() bool {
	return c.conn != nil && c.page != nil && c.active == "" && c.slot.phase == "" && !c.requested && c.tooling == 0 && !c.speaking
}

// ready reports whether a Dyna item may go out now: the call is idle and
// owes the owner no reply.
// Dyna items also wait while the page still plays Voice's audio, as qwen's
// announcement window does: new items wait for its playback to end.
func (c *Call) ready() bool { return c.idle() && !c.owed && len(c.onPage) == 0 }

// next requests the response the call owes, or else sends the next Dyna
// item. The call creates every response itself, one at a time, so they
// never compete for the provider's response slot.
func (c *Call) next() {
	if c.correction != "" && c.idle() && !c.owed {
		c.send(c.provider.Protocol.UserText(c.correction))
		c.send(c.provider.Protocol.ResponseCreate(""))
		c.requested, c.asked, c.correction, c.correctionAsked = true, "", "", true
		c.occupy()
		return
	}
	if c.owed && c.idle() {
		c.owed = false
		c.send(c.provider.Protocol.ResponseCreate(""))
		c.requested, c.asked = true, ""
		c.occupy()
		return
	}
	c.queue.pump()
}

// say sends one Dyna item for Voice to tell the owner.
func (c *Call) say(key, text string) {
	p := c.provider.Protocol
	c.send(p.UserText(text))
	c.send(p.ResponseCreate(key))
	c.requested, c.asked = true, key
	c.occupy()
}

// send writes event to the provider. While the call redials, it is
// dropped; a write error is left to the reader, which sees the drop too.
func (c *Call) send(event any) {
	if c.conn != nil && event != nil { // nil: the protocol has nothing to send
		_ = c.conn.Send(event)
	}
}

func (c *Call) event(ctx context.Context, ev Event) {
	switch ev.Kind {
	case ResponseStarted:
		c.active, c.requested = ev.ResponseID, false
		if c.correctionAsked {
			c.correcting, c.correctionAsked = ev.ResponseID, false
		}
		if ev.Key == "" {
			// A dialect without response metadata: the call creates one
			// response at a time, so this is the one it asked for last.
			ev.Key = c.asked
		}
		c.running()
		c.keys[ev.ResponseID] = ev.Key
		if c.cancelNext {
			c.cancelNext = false
			c.cancel()
		}
	case ResponseDone:
		if ev.ResponseID == c.active {
			c.active = ""
		}
		if ev.Text == "completed" {
			c.safetyRecoveries = 0
		}
		c.releaseSlot()
		if !c.speaking && len(c.onPage) == 0 {
			c.setState("listening")
		}
	case AudioDelta, AudioDone:
		if c.cancelled[ev.ResponseID] {
			// Late audio of a response the owner interrupted. An item the
			// page never got is dropped from the conversation whole.
			if !c.known[ev.ItemID] {
				c.known[ev.ItemID] = true
				c.send(c.provider.Protocol.Truncate(ev.ItemID, 0))
			}
			return
		}
		c.known[ev.ItemID] = true
		if c.slot.phase == "running" {
			c.running()
		}
		if ev.Kind == AudioDone {
			c.toPage(PageEvent{Type: "audio_done", ID: ev.ItemID})
			return
		}
		if _, ok := c.responses[ev.ItemID]; !ok {
			c.responses[ev.ItemID] = ev.ResponseID
			c.onPage = append(c.onPage, ev.ItemID)
		}
		c.audio[ev.ItemID] += len(ev.Audio)
		c.setState("speaking")
		c.toPage(PageEvent{Type: "audio", ID: ev.ItemID, Audio: ev.Audio})
	case SpeechStarted:
		c.correction = "" // the owner speaks: their new turn is the answer
		c.speaking = true
		c.interrupt()
		c.setState("listening")
		if c.muted {
			// The owner spoke and muted before the provider heard the
			// speech: the mute still ends the turn.
			c.send(c.provider.Protocol.AudioCommit())
		}
	case Activity:
		// A response that thinks is alive: its inactivity guard starts again.
		if c.slot.phase == "running" && ev.ResponseID == c.active {
			c.running()
		}
	case Interrupted:
		// The server cut Voice's reply for the owner: stop playback too.
		c.interrupt()
	case SpeechStopped:
		c.setState("thinking")
	case Committed:
		// The owner's turn is final: answer it before any Dyna item.
		c.speaking, c.owed = false, true
	case ToolCall:
		silent := silentTool(c.tools, ev.Name)
		c.tooling++
		if !silent {
			c.setState("thinking")
			c.toPage(PageEvent{Type: "tool", ID: ev.CallID, Name: ev.Name, Text: string(ev.Args), State: "running"})
		}
		gen := c.gen
		key := c.keys[ev.ResponseID]
		if ev.ResponseID == "" {
			key = c.keys[c.active]
		}
		go func() {
			out := RunTool(ctx, c.tools, ev)
			c.do(func() {
				c.tooling--
				state := "done"
				if failed(out) {
					state = "failed"
				}
				if !silent {
					c.toPage(PageEvent{Type: "tool", ID: ev.CallID, Name: ev.Name, State: state})
				}
				if gen != c.gen {
					return // a dropped connection asked; the new one does not wait for its output
				}
				quiet := !failed(out) && (silent || laterTool(c.tools, ev.Name))
				output := c.provider.Protocol.FunctionOutput
				if p, ok := c.provider.Protocol.(interface{ QuietFunctionOutput(string, string) any }); quiet && ok {
					output = p.QuietFunctionOutput
				}
				c.send(output(ev.CallID, out))
				if silent && !failed(out) {
					c.queue.acknowledged(key)
				}
				// Accepted work answers later. Keep its receipt as context
				// without requesting an acknowledgement response.
				if !quiet {
					c.owed = true
				}
			})
		}()
	case OwnerPartial:
		c.toPage(PageEvent{Type: "transcript", Text: ev.Text})
	case OwnerTranscript:
		c.toPage(PageEvent{Type: "transcript", Text: ev.Text, Final: true})
		c.transcript(true, ev.Text)
	case VoiceTranscript:
		// A model reply that writes a reserved protocol tag is not
		// recorded, and Voice is asked once to answer again, unless the
		// owner already speaks again or the reply is itself a correction.
		if fix := guardReply(ev.Text); fix != "" && c.keys[ev.ResponseID] == "" {
			// A provider that takes no client replies cannot be asked again;
			// its reply is still not recorded.
			if !c.speaking && ev.ResponseID != c.correcting && !c.cancelled[ev.ResponseID] && !c.provider.Caps.NoClientResponses {
				c.correction = fix
			}
			return
		}
		c.transcript(false, ev.Text)
	case Error:
		// A refused response.create starts no response; cancelling a
		// response that already ended is no fault. A refusal because a
		// response is active, when the call knows of none, means the
		// provider holds the slot: it is guarded like one asked for.
		c.requested = false
		// A busy refusal keeps the slot's guard as it is, or starts one if
		// the call tracked none; it never restarts a guard.
		if ev.Code == "conversation_already_has_active_response" {
			c.occupy()
		} else if c.active == "" && c.slot.phase == "pending" {
			c.releaseSlot()
		}
		if c.provider.ContentRefused != nil && c.provider.ContentRefused(ev.Code, ev.Text) {
			c.refusedContent(ev.Text)
		} else if ev.Code != "response_cancel_not_active" {
			c.toPage(PageEvent{Type: "error", Message: ev.Text})
		}
	}
}

// interrupt stops Voice: it cancels the active response and tells the page
// to stop playback. The page answers with how much of each item it played,
// and playbackEnded truncates the rest.
func (c *Call) interrupt() {
	c.correction = "" // the owner stopped Voice: nothing restarts it
	switch {
	case c.active != "":
		c.cancel()
	case c.requested:
		c.cancelNext = true
	}
	if len(c.onPage) == 0 {
		return
	}
	for _, id := range c.onPage {
		c.cleared[id] = true
	}
	c.toPage(PageEvent{Type: "clear", ID: c.onPage[0]})
}

// cancel cancels the active response and drops its audio still to come.
func (c *Call) cancel() {
	c.cancelled[c.active] = true
	c.send(c.provider.Protocol.ResponseCancel())
}

func (c *Call) fromPage(m PageMessage) {
	switch m.Type {
	case "audio":
		// The page sends PageRate audio; the provider takes its own rate.
		// Audio that waits for a redial is kept at that rate already.
		audio := resample(m.Audio, PageRate, c.provider.InputRate)
		if len(audio) == 0 {
			return // too short to hold one sample at the provider's rate
		}
		if c.conn == nil {
			c.pending = append(c.pending, audio)
			if len(c.pending) > MaxPendingAudio {
				c.pending = c.pending[len(c.pending)-MaxPendingAudio:]
			}
			return
		}
		c.send(c.provider.Protocol.AudioAppend(audio))
		c.streamed = true
	case "mute":
		// Muting ends the owner's turn now, without waiting for silence. If
		// the provider has not heard the speech yet, SpeechStarted ends it.
		if p, ok := c.provider.Protocol.(InputMuter); ok {
			if m.Muted != c.muted {
				c.send(p.Mute(m.Muted))
			}
			c.muted = m.Muted
			c.keepStepping()
			return
		}
		c.muted = m.Muted
		if c.muted && (c.speaking || c.provider.Caps.EndsStreamOnMute && c.streamed) {
			c.send(c.provider.Protocol.AudioCommit())
			c.streamed = false
		}
	case "hangup":
		c.err = errHangup
	case "cancel":
		c.interrupt()
	case "playback":
		switch m.State {
		case "started":
			if _, ok := c.responses[m.ID]; !ok {
				return // audio of a dropped connection
			}
			c.playing[m.ID] = c.now()
			c.queue.started(m.ID, c.keys[c.responses[m.ID]])
		case "ended":
			c.playbackEnded(m.ID, time.Duration(m.PlayedMs)*time.Millisecond)
		}
	}
}

// playbackEnded notes that the page stopped playing item id after played.
// When the page stopped early on a clear, the provider forgets the rest, so
// Voice never remembers words the owner did not hear. The provider may hold
// more of a cancelled response than the page got, so its items are always
// cut at played.
func (c *Call) playbackEnded(id string, played time.Duration) {
	c.queue.ended(id)
	if c.cleared[id] && (played < c.length(id) || c.cancelled[c.responses[id]]) {
		c.send(c.provider.Protocol.Truncate(id, played))
	}
	for i, item := range c.onPage {
		if item == id {
			c.onPage = append(c.onPage[:i], c.onPage[i+1:]...)
			break
		}
	}
	delete(c.cleared, id)
	delete(c.playing, id)
	delete(c.audio, id)
	delete(c.responses, id)
	if len(c.onPage) == 0 && c.active == "" && !c.speaking {
		c.setState("listening")
	}
}

func (c *Call) setState(s string) {
	if s != c.state {
		c.state = s
		c.toPage(PageEvent{Type: "state", State: s})
	}
}
