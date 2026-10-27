package voice

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/voice/agentwire"
	"dyna-agent/internal/voice/realtime"
)

// A live voice session for agents: a program sends microphone audio in and
// reads reply audio, transcripts and tool calls out, and plays the audio
// where it wants. The engine holds the provider connection, so the agent
// never sees the account token. A session posts nothing, wakes nobody and
// keeps no transcript; the caller (the engine) tells the room only that it
// started and ended.

// checkRequest validates req and fills its defaults.
func checkRequest(req *agentwire.SessionRequest) error {
	if req.Rate == 0 {
		req.Rate = agentwire.DefaultSessionRate
	}
	if req.Rate < agentwire.MinSessionRate || req.Rate > agentwire.MaxSessionRate {
		return fmt.Errorf("the sample rate %d is outside %d to %d Hz", req.Rate, agentwire.MinSessionRate, agentwire.MaxSessionRate)
	}
	if req.MaxMinutes == 0 {
		req.MaxMinutes = agentwire.DefaultSessionMinutes
	}
	if req.MaxMinutes < 1 || req.MaxMinutes > agentwire.MaxSessionMinutes {
		return fmt.Errorf("--max-minutes must be from 1 to %d", agentwire.MaxSessionMinutes)
	}
	eagerness := strings.ToLower(strings.TrimSpace(req.VADEagerness))
	switch eagerness {
	case "", "low", "medium", "high", "auto":
		req.VADEagerness = eagerness
	default:
		return fmt.Errorf("--vad-eagerness must be low, medium, high or auto")
	}
	switch agentwire.TurnMode(strings.ToLower(strings.TrimSpace(string(req.Turns)))) {
	case "", agentwire.TurnsAuto:
		req.Turns = agentwire.TurnsAuto
	case agentwire.TurnsManual:
		req.Turns = agentwire.TurnsManual
	default:
		return fmt.Errorf("--turns must be auto or manual")
	}
	if req.Turns == agentwire.TurnsManual && req.VADEagerness != "" {
		return fmt.Errorf("--vad-eagerness conflicts with --turns manual; drop --vad-eagerness")
	}
	if req.Speed != 0 && (req.Speed != req.Speed || req.Speed < agentwire.MinOutputSpeed || req.Speed > agentwire.MaxOutputSpeed) {
		return fmt.Errorf("--speed must be from %g to %g", agentwire.MinOutputSpeed, agentwire.MaxOutputSpeed)
	}
	req.ReplyInstructions = strings.TrimSpace(req.ReplyInstructions)
	if strings.TrimSpace(req.Instructions) == "" {
		req.Instructions = defaultSessionInstructions
	}
	seen := map[string]bool{}
	for _, t := range req.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return errors.New("every tool needs a name")
		}
		if seen[t.Name] {
			return fmt.Errorf("the tool %q is listed twice", t.Name)
		}
		seen[t.Name] = true
		switch t.After {
		case "", agentwire.ToolAfterReply, agentwire.ToolAfterLater, agentwire.ToolAfterSilent:
		default:
			return fmt.Errorf("tool %q has after %q; use %q, %q or %q", t.Name, t.After, agentwire.ToolAfterReply, agentwire.ToolAfterLater, agentwire.ToolAfterSilent)
		}
	}
	return nil
}

// SessionEnd tells how a session that started ended.
type SessionEnd struct {
	Provider string
	Elapsed  time.Duration
	Reason   agentwire.EndReason
}

// SessionIO connects a session to its caller. Started, if set, runs once the
// provider accepts the session and before the first line goes out.
type SessionIO struct {
	// In delivers the agent's lines. A closed channel is the end of its input.
	In  <-chan agentwire.InMessage
	Out func(agentwire.OutMessage) error
	// Started gets the provider's key.
	Started func(provider string)
}

// Sessions runs live voice sessions for agents, one at a time. The zero value
// is not ready: use NewSessions.
type Sessions struct {
	provider func(name string) (realtime.Provider, error)
	dial     func(context.Context, realtime.Provider, realtime.Session) (sayConn, error)
	// enabled returns an error when the owner turned agent voice off. Nil
	// means on.
	enabled func() error
	// now and after are the clock; tests replace them.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
	slot  chan struct{}
}

// NewSessions returns Sessions that use the owner's voice settings.
func NewSessions() *Sessions {
	return &Sessions{
		provider: func(name string) (realtime.Provider, error) { return sayProvider(settings.Load, name) },
		dial: func(ctx context.Context, p realtime.Provider, s realtime.Session) (sayConn, error) {
			return realtime.Dial(ctx, p, s)
		},
		enabled: func() error { return agentVoiceEnabled(settings.Load) },
		now:     time.Now,
		after:   time.After,
		slot:    make(chan struct{}, 1),
	}
}

// defaultSessionInstructions are used when the agent gives none.
const defaultSessionInstructions = "You are a friendly voice assistant. Keep your replies short."

// sessionEvent is one provider event, or the error that ended the reads.
type sessionEvent struct {
	ev  realtime.Event
	err error
}

// Run runs one session until it ends. It returns an error, and no end, when
// the session never started: the setting is off, another session runs, the
// request is invalid or the provider refused it. After Started has run, it
// returns the end and a nil error.
func (s *Sessions) Run(ctx context.Context, req agentwire.SessionRequest, io SessionIO) (SessionEnd, error) {
	if s.enabled != nil {
		if err := s.enabled(); err != nil {
			return SessionEnd{}, err
		}
	}
	if err := checkRequest(&req); err != nil {
		return SessionEnd{}, err
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	default:
		return SessionEnd{}, errors.New("voice is busy: an agent voice session is already running; try again when it ends")
	}
	p, err := s.provider(req.Provider)
	if err != nil {
		return SessionEnd{}, err
	}
	if len(req.Tools) > 0 && !p.Caps.FunctionCalling {
		return SessionEnd{}, fmt.Errorf("%s cannot call tools; choose another provider with --provider, or drop --tools", p.Label)
	}
	if req.VADEagerness != "" && !p.Caps.VADEagerness {
		return SessionEnd{}, fmt.Errorf("%s does not take --vad-eagerness; choose another provider with --provider, or drop the flag", p.Label)
	}
	if req.Turns == agentwire.TurnsManual && (!p.Caps.ManualTurns || !p.Caps.ManualCommit) {
		return SessionEnd{}, fmt.Errorf("%s does not take --turns manual; choose another provider with --provider, or use --turns auto", p.Label)
	}
	if req.Speed != 0 && !p.Caps.OutputSpeed {
		return SessionEnd{}, fmt.Errorf("%s does not take --speed; choose another provider with --provider, or drop the flag", p.Label)
	}
	if req.ReplyInstructions != "" {
		_, instructed := p.Protocol.(realtime.InstructedProtocol)
		if !p.Caps.ResponseInstructions || !instructed {
			return SessionEnd{}, fmt.Errorf("%s does not take --reply-instructions; choose another provider with --provider, or drop the flag", p.Label)
		}
	}
	tools := make([]realtime.Tool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = realtime.Tool{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
			Later:  t.After == agentwire.ToolAfterLater,
			Silent: t.After == agentwire.ToolAfterSilent,
		}
	}
	// Dial can take a while: the context ends it, not the session's limits.
	conn, err := s.dial(ctx, p, realtime.Session{Instructions: req.Instructions, Tools: tools, Voice: req.Voice, Language: "en", VADEagerness: req.VADEagerness, ManualTurns: req.Turns == agentwire.TurnsManual, Speed: req.Speed})
	if err != nil {
		return SessionEnd{}, err
	}
	defer conn.Close()
	if io.Started != nil {
		io.Started(p.Key)
	}
	l := &sessionLoop{s: s, req: req, p: p, conn: conn, io: io, started: s.now(), pending: map[string]string{}, dropped: map[string]bool{}}
	reason := l.run(ctx)
	end := SessionEnd{Provider: p.Key, Elapsed: s.now().Sub(l.started), Reason: reason}
	_ = io.Out(agentwire.OutMessage{Type: agentwire.OutClosed, Reason: reason})
	return end, nil
}

// sessionLoop is the state of one running session. One goroutine uses it.
type sessionLoop struct {
	s       *Sessions
	req     agentwire.SessionRequest
	p       realtime.Provider
	conn    sayConn
	io      SessionIO
	started time.Time
	// lastAudio is when input audio last came.
	lastAudio time.Time
	// active is the response that plays, empty when none does.
	active string
	// creating is true after response.create until that request ends.
	// It is set before the provider says the response started, so a second
	// reply waits while l.active is still empty.
	creating bool
	// owed: one follow-up reply waits for the in-flight response to end.
	// owedInstructions is the style for that reply, empty when it has none.
	owed             bool
	owedInstructions string
	// dropped holds the cancelled responses whose late audio is not sent.
	dropped map[string]bool
	// pending holds the tool calls that wait for the agent's result, by tool name.
	pending map[string]string
	// timeouts delivers the ID of a tool call whose time ran out.
	timeouts chan string
	// commitAsked means a commit line already asked for a reply, and its
	// provider echo has not arrived. That echo must not ask again. Speech
	// starting, or a provider error, clears it. A missing echo then cannot
	// swallow the next real turn. ResponseDone does not clear it: the echo
	// can arrive after the reply ends, and that echo is not a new turn.
	commitAsked bool
	// heardSpeech is true after speech started, or after a non-silent
	// audio chunk, until the turn is committed. Keepalive silence does
	// not set it, so a later client commit does not ask again.
	heardSpeech bool
}

// run reads the agent and the provider until the session ends.
func (l *sessionLoop) run(ctx context.Context) agentwire.EndReason {
	done := make(chan struct{})
	defer close(done)
	events := make(chan sessionEvent)
	go func() {
		for {
			ev, err := l.conn.Next()
			select {
			case events <- sessionEvent{ev, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	l.timeouts = make(chan string, len(l.req.Tools)+16)
	limit := l.s.after(time.Duration(l.req.MaxMinutes) * time.Minute)
	l.lastAudio = l.s.now()
	idle := l.s.after(agentwire.SessionIdle)
	if l.io.Out(agentwire.OutMessage{Type: agentwire.OutReady, Rate: l.req.Rate}) != nil {
		return agentwire.EndCancelled
	}
	for {
		select {
		case <-ctx.Done():
			return agentwire.EndCancelled
		case <-limit:
			return agentwire.EndTimeLimit
		case <-idle:
			if wait := agentwire.SessionIdle - l.s.now().Sub(l.lastAudio); wait > 0 {
				idle = l.s.after(wait)
				continue
			}
			return agentwire.EndIdle
		case id := <-l.timeouts:
			if reason, ended := l.toolTimedOut(id); ended {
				return reason
			}
		case m, ok := <-l.io.In:
			if !ok {
				return agentwire.EndInputClosed
			}
			if reason, ended := l.fromAgent(m); ended {
				return reason
			}
		case r := <-events:
			if r.err != nil {
				if ctx.Err() != nil {
					return agentwire.EndCancelled
				}
				return agentwire.EndProviderClosed
			}
			if reason, ended := l.fromProvider(ctx, r.ev, done); ended {
				return reason
			}
		}
	}
}

// fail tells the agent about a problem and keeps the session.
func (l *sessionLoop) fail(format string, args ...any) (agentwire.EndReason, bool) {
	if l.io.Out(agentwire.OutMessage{Type: agentwire.OutError, Message: fmt.Sprintf(format, args...)}) != nil {
		return agentwire.EndCancelled, true
	}
	return "", false
}

// send writes to the provider. A failed write ends the session.
func (l *sessionLoop) send(event any) (agentwire.EndReason, bool) {
	if err := l.conn.Send(event); err != nil {
		return agentwire.EndProviderClosed, true
	}
	return "", false
}

// reply asks for a response with no extra style. Every provider has one
// response slot, so a create that was sent, or a response that started and
// has not ended, holds it. A second reply queues one follow-up instead of
// sending now.
func (l *sessionLoop) reply() (agentwire.EndReason, bool) {
	return l.ask("")
}

// ask asks for a response. instructions is style for that reply only.
// A busy slot keeps one follow-up, and that follow-up keeps these instructions.
func (l *sessionLoop) ask(instructions string) (agentwire.EndReason, bool) {
	if l.p.Caps.NoClientResponses {
		return "", false // the provider answers on its own
	}
	if l.creating || l.active != "" {
		l.owed = true
		l.owedInstructions = instructions
		return "", false
	}
	return l.create(instructions)
}

// create sends response.create. A non-empty instructions value uses the
// provider's per-response instructions. Empty instructions use the session
// default, when one is set. The caller already checked that the provider
// can take them.
func (l *sessionLoop) create(instructions string) (agentwire.EndReason, bool) {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		instructions = l.req.ReplyInstructions
	}
	event := l.p.Protocol.ResponseCreate("")
	if instructions != "" {
		event = l.p.Protocol.(realtime.InstructedProtocol).ResponseInstructed("", instructions)
	}
	if reason, ended := l.send(event); ended {
		return reason, true
	}
	l.creating = true
	return "", false
}

// say speaks the text just added. Replace cancels the active response first.
// Append never overlaps: it waits until the active response ends.
// instructions is style for this reply only.
func (l *sessionLoop) say(mode agentwire.SayMode, instructions string) (agentwire.EndReason, bool) {
	if l.active == "" {
		return l.ask(instructions)
	}
	if mode == agentwire.SayReplace {
		l.dropped[l.active] = true
		if reason, ended := l.stopPlayback(); ended {
			return reason, true
		}
	}
	if l.p.Caps.NoClientResponses {
		return "", false
	}
	l.owed = true
	l.owedInstructions = instructions
	return "", false
}

// releaseOwed sends the one follow-up reply and its saved style.
func (l *sessionLoop) releaseOwed() (agentwire.EndReason, bool) {
	instructions := l.owedInstructions
	l.owed = false
	l.owedInstructions = ""
	return l.create(instructions)
}

// stopPlayback cancels the active response and tells the agent to stop playing.
func (l *sessionLoop) stopPlayback() (agentwire.EndReason, bool) {
	if l.active != "" && !l.p.Caps.NoClientResponses {
		if reason, ended := l.send(l.p.Protocol.ResponseCancel()); ended {
			return reason, true
		}
	}
	l.owed = false
	return l.emit(agentwire.OutMessage{Type: agentwire.OutSpeechStart})
}

// emit sends m and reports a failed write as the session's end.
func (l *sessionLoop) emit(m agentwire.OutMessage) (agentwire.EndReason, bool) {
	if l.io.Out(m) != nil {
		return agentwire.EndCancelled, true
	}
	return "", false
}

// fromAgent handles one line from the agent.
func (l *sessionLoop) fromAgent(m agentwire.InMessage) (agentwire.EndReason, bool) {
	switch m.Type {
	case agentwire.InAudio:
		l.lastAudio = l.s.now()
		pcm := realtime.Resample(m.PCM, l.req.Rate, l.p.InputRate)
		if len(pcm) == 0 {
			return "", false
		}
		if pcmHasSpeech(pcm) {
			l.heardSpeech = true
		}
		return l.send(l.p.Protocol.AudioAppend(pcm))
	case agentwire.InText, agentwire.InSay:
		if l.p.Caps.NoClientResponses {
			return l.fail("%s cannot take text; it only hears audio", l.p.Label)
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			return l.fail("the %s line has no text", m.Type)
		}
		instructions, reason, ended, stopped := l.lineInstructions(m.Instructions)
		if stopped {
			return reason, ended
		}
		if m.Type == agentwire.InSay && m.Mode != "" && m.Mode != agentwire.SayAppend && m.Mode != agentwire.SayReplace {
			return l.fail("unknown say mode %q; use %q or %q", m.Mode, agentwire.SayAppend, agentwire.SayReplace)
		}
		// A text line that does not ask for a reply has no response to style.
		if m.Type == agentwire.InText && instructions != "" && !m.Respond {
			return l.fail("instructions on a text line need \"respond\":true, or use a say line")
		}
		if m.Type == agentwire.InSay {
			text = "Say exactly this, word for word, and nothing else: " + text
		}
		if reason, ended := l.send(l.p.Protocol.UserText(text)); ended {
			return reason, true
		}
		if m.Type == agentwire.InSay {
			return l.say(m.Mode, instructions)
		}
		// respond asks for one reply and waits for speech that plays,
		// the same queue as say mode append. The default stays silent.
		if m.Respond {
			return l.say(agentwire.SayAppend, instructions)
		}
		return "", false
	case agentwire.InImage:
		return l.image(m)
	case agentwire.InToolResult:
		name, ok := l.pending[m.CallID]
		if !ok {
			return l.fail("there is no tool call %q waiting for a result", m.CallID)
		}
		delete(l.pending, m.CallID)
		return l.toolOutput(m.CallID, m.Output, l.toolAfter(name))
	case agentwire.InInterrupt:
		return l.stopPlayback()
	case agentwire.InCommit:
		instructions, reason, ended, stopped := l.lineInstructions(m.Instructions)
		if stopped {
			return reason, ended
		}
		return l.commit(instructions)
	}
	return l.fail("unknown line type %q", m.Type)
}

// lineInstructions returns style for this line. Empty is allowed. A
// provider that cannot set per-response instructions gets an error line,
// and stopped is true so the caller sends nothing.
func (l *sessionLoop) lineInstructions(raw string) (instructions string, reason agentwire.EndReason, ended, stopped bool) {
	instructions = strings.TrimSpace(raw)
	if instructions == "" {
		return "", "", false, false
	}
	if l.p.Caps.ResponseInstructions {
		if _, ok := l.p.Protocol.(realtime.InstructedProtocol); ok {
			return instructions, "", false, false
		}
	}
	reason, ended = l.fail("%s does not take per-response instructions; drop the instructions field, or choose another provider with --provider", l.p.Label)
	return "", reason, ended, true
}

// commit ends the buffered user turn and asks for one reply. instructions
// is style for that reply only; empty uses the session default. The reply
// uses the same one-response queue as every other ask. The provider's
// committed echo of this line must not ask again.
func (l *sessionLoop) commit(instructions string) (agentwire.EndReason, bool) {
	if !l.p.Caps.ManualCommit {
		return l.fail("%s cannot commit a turn by hand", l.p.Label)
	}
	event := l.p.Protocol.AudioCommit()
	if event == nil {
		return l.fail("%s cannot commit a turn by hand", l.p.Label)
	}
	// The provider may already have committed this audio. An empty commit
	// is an error and would queue a second reply to the same turn.
	if !l.heardSpeech {
		return "", false
	}
	if reason, ended := l.send(event); ended {
		return reason, true
	}
	l.heardSpeech = false
	l.commitAsked = true
	return l.ask(instructions)
}

// pcmHasSpeech reports whether pcm has a non-zero sample. The voice
// guide's keepalive is an all-zero chunk, and that must not start a turn.
func pcmHasSpeech(pcm []byte) bool {
	for i := 0; i+1 < len(pcm); i += 2 {
		if int16(binary.LittleEndian.Uint16(pcm[i:])) != 0 {
			return true
		}
	}
	return false
}

// image adds an image to the conversation without a reply.
func (l *sessionLoop) image(m agentwire.InMessage) (agentwire.EndReason, bool) {
	proto, ok := l.p.Protocol.(realtime.ImageProtocol)
	if !ok || !l.p.Caps.ImageInput {
		return l.fail("%s does not take images; choose another provider with --provider", l.p.Label)
	}
	if m.Mime != "image/png" && m.Mime != "image/jpeg" {
		return l.fail("an image must be PNG or JPEG, not %q", m.Mime)
	}
	if len(m.Image) == 0 || len(m.Image) > agentwire.MaxImageBytes {
		return l.fail("an image must have 1 byte to %d MB", agentwire.MaxImageBytes>>20)
	}
	return l.send(proto.UserImage(m.Mime, m.Image))
}

// toolAfter is how a result for name should continue. An unknown name replies.
func (l *sessionLoop) toolAfter(name string) agentwire.ToolAfter {
	for _, t := range l.req.Tools {
		if t.Name == name {
			return t.After
		}
	}
	return agentwire.ToolAfterReply
}

// toolOutput gives the model a tool's output. reply asks it to go on. later and
// silent keep the result as context and ask for no reply, the same marks Dyna
// Voice uses. A provider that can complete a tool quietly, such as Google, does
// so, so the result does not resume speech by itself. A silent success sends an
// empty result. An error, including a timeout, still asks for a reply.
func (l *sessionLoop) toolOutput(callID, output string, after agentwire.ToolAfter) (agentwire.EndReason, bool) {
	quiet := after == agentwire.ToolAfterLater || after == agentwire.ToolAfterSilent
	if quiet && after == agentwire.ToolAfterSilent {
		output = ""
	}
	send := l.p.Protocol.FunctionOutput
	if quiet {
		if p, ok := l.p.Protocol.(interface{ QuietFunctionOutput(string, string) any }); ok {
			send = p.QuietFunctionOutput
		}
	}
	if reason, ended := l.send(send(callID, output)); ended {
		return reason, true
	}
	if quiet {
		return "", false
	}
	return l.reply()
}

// toolTimedOut gives the model an error result for a call the agent did not
// answer in time.
func (l *sessionLoop) toolTimedOut(callID string) (agentwire.EndReason, bool) {
	if _, ok := l.pending[callID]; !ok {
		return "", false
	}
	delete(l.pending, callID)
	if reason, ended := l.fail("the tool call %q got no result in %d seconds; the model got an error", callID, int(realtime.ToolTimeout.Seconds())); ended {
		return reason, true
	}
	out, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{"the tool did not answer in time"})
	return l.toolOutput(callID, string(out), agentwire.ToolAfterReply)
}

// fromProvider handles one provider event.
func (l *sessionLoop) fromProvider(ctx context.Context, ev realtime.Event, done <-chan struct{}) (agentwire.EndReason, bool) {
	switch ev.Kind {
	case realtime.ResponseStarted:
		l.active = ev.ResponseID
	case realtime.AudioDelta:
		if l.dropped[ev.ResponseID] {
			return "", false
		}
		pcm := realtime.Resample(ev.Audio, l.p.OutputRate, l.req.Rate)
		return l.emit(agentwire.OutMessage{Type: agentwire.OutAudio, ResponseID: ev.ResponseID, PCM: pcm})
	case realtime.SpeechStarted, realtime.Interrupted:
		// A new turn started. The pending commit echo, if it never comes,
		// must not swallow this turn's commit. Speech starting is real
		// speech even when the chunks after it are silence.
		l.commitAsked = false
		if ev.Kind == realtime.SpeechStarted {
			l.heardSpeech = true
		}
		// Manual mode does not barge in. Audio that arrives while a reply
		// plays, and a speech event for that audio, leave the reply running.
		// The client sends interrupt. An Interrupted event means the server
		// already stopped the reply, so the client still has to drop playback.
		if l.req.Turns == agentwire.TurnsManual && ev.Kind == realtime.SpeechStarted {
			return "", false
		}
		return l.stopPlayback()
	case realtime.SpeechStopped:
		// Turn detection already ended this turn. Audio still in flight
		// belongs to the commit the provider is about to send. In manual
		// mode the provider must not end the turn, so this event is ignored.
		if l.req.Turns == agentwire.TurnsManual {
			return "", false
		}
		l.heardSpeech = false
	case realtime.Committed:
		// In manual mode only the client's commit asks for a reply. An
		// unsolicited provider commit must not end the turn or clear the
		// speech the client still has to commit.
		if l.req.Turns == agentwire.TurnsManual && !l.commitAsked {
			return "", false
		}
		l.heardSpeech = false
		if l.commitAsked {
			l.commitAsked = false
			return "", false
		}
		return l.reply()
	case realtime.OwnerTranscript:
		return l.emit(agentwire.OutMessage{Type: agentwire.OutTranscript, Role: agentwire.RoleUser, Text: ev.Text})
	case realtime.VoiceTranscript:
		return l.emit(agentwire.OutMessage{Type: agentwire.OutTranscript, Role: agentwire.RoleAssistant, Text: ev.Text})
	case realtime.ToolCall:
		l.pending[ev.CallID] = ev.Name
		expired := l.s.after(realtime.ToolTimeout)
		go func() {
			select {
			case <-expired:
				select {
				case l.timeouts <- ev.CallID:
				case <-done:
				}
			case <-done:
			case <-ctx.Done():
			}
		}()
		return l.emit(agentwire.OutMessage{Type: agentwire.OutToolCall, CallID: ev.CallID, Name: ev.Name, Args: ev.Args})
	case realtime.ResponseDone:
		// A done event for the response that plays, or while none has started,
		// frees the slot. A done event for some other response does not.
		if ev.ResponseID == l.active || l.active == "" {
			l.active = ""
			l.creating = false
		}
		if reason, ended := l.emit(agentwire.OutMessage{Type: agentwire.OutResponseDone, ResponseID: ev.ResponseID}); ended {
			return reason, true
		}
		if l.owed && !l.creating && l.active == "" {
			return l.releaseOwed()
		}
	case realtime.Error:
		// A rejected commit sends no echo. Drop the pending mark so the
		// next real turn still gets a reply.
		l.commitAsked = false
		// OpenAI and Azure reject a commit of an empty or too-small buffer
		// with this code. The message is "buffer is empty" or "buffer too
		// small ... 0.00ms". The provider already ended the turn, or the
		// buffer had nothing to commit. That is not a client error, and it
		// must not ask for another reply.
		if ev.Code == "input_audio_buffer_commit_empty" {
			if l.active == "" {
				l.creating = false
			}
			return "", false
		}
		// A busy refusal means a response still holds the slot. Any other
		// error ends a create the provider has not started.
		busy := ev.Code == "conversation_already_has_active_response"
		if busy {
			l.creating = true
		} else {
			l.creating = false
		}
		if reason, ended := l.fail("%s", ev.Text); ended {
			return reason, true
		}
		if !busy && l.active == "" && l.owed {
			return l.releaseOwed()
		}
	}
	return "", false
}
