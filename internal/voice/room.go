package voice

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

// RoomLink posts the owner's requests to root and the call's transcript.
type RoomLink struct {
	svc           *notify.Service
	rec           room.Record
	engineContext context.Context
	resumeStarted time.Time
	replayCursor  string
}

// OpenRoom links voice turns to the named room, or the current one when name is empty.
func OpenRoom(name string) (*RoomLink, error) {
	return LinkRoom(notify.Default(), name)
}

// LinkRoom links voice turns to the room named or identified by ref, or the
// current one when ref is empty, through svc.
func LinkRoom(svc *notify.Service, ref string) (*RoomLink, error) {
	rec, err := svc.Rooms.ResolveContext(ref, false)
	if err != nil {
		return nil, err
	}
	return &RoomLink{svc: svc, rec: rec}, nil
}

// Name is the room's name.
func (r *RoomLink) Name() string { return r.rec.Name }

// ID is the room's ID.
func (r *RoomLink) ID() string { return r.rec.ID }

// Ask posts the owner's spoken request, in the owner's words, from Voice to
// @root, and returns the post ID. Like the owner's typed request, it starts
// root when none is open, and root owes it a report.
func (r *RoomLink) Ask(ctx context.Context, text string) (string, error) {
	return r.toRoot(ctx, notify.PostInput{Text: text})
}

func (r *RoomLink) toRoot(ctx context.Context, in notify.PostInput) (string, error) {
	in.Sender, in.Mentions = notify.Sender{Kind: notify.SenderVoice, Room: r.rec.Name}, []string{"@root"}
	res, err := r.svc.Post(ctx, in)
	if res.Message.ID == "" && err == nil {
		err = errors.New("the post was not stored")
	}
	return res.Message.ID, err
}

// Spoken records one transcript line of the call: the owner's when owner is
// true, otherwise Voice's.
func (r *RoomLink) Spoken(owner bool, text string) error {
	_, err := r.svc.Rooms.Post(r.rec.ID, r.callPost(owner, room.PostInput{Spoken: true, Text: text}))
	return err
}

// callPost completes a post of the owner-Voice conversation. Kind "voice"
// keeps it out of the room feed and root's [OWNER DMS] block, and never
// wakes root. Root sees only Voice's task posts to it.
func (r *RoomLink) callPost(owner bool, in room.PostInput) room.PostInput {
	voice := r.rec.Name + ":" + room.VoiceAgent
	in.From, in.To = voice, []string{r.rec.Name}
	if owner {
		// The owner's own speech, transcribed: a person made it.
		in.From, in.To, in.Human = r.rec.Name, []string{voice}, true
	}
	in.Kind, in.Source = "voice", room.SourceVoice
	return in
}

// Speaker is the call's side of Watch.
type Speaker interface {
	Speak(key, topic, text string)
	Context(text string)
}

// Watch hands the call what root says to the owner: each post to
// @voice-call from since on, each of root's questions to the owner while it
// is open, and the final result of each root turn that reports to the owner,
// the oldest first. A question's topic is its ID, so a newer post of the same
// question replaces one not yet said. A result whose turn root already
// relayed to @voice-call is not said twice. While root works on a dispatched
// request, its agent traffic is silent context. It runs until ctx ends, so
// the call hears results only while it is live.
func (r *RoomLink) Watch(ctx context.Context, since time.Time, call Speaker) error {
	voice := r.rec.Name + ":" + room.VoiceAgent
	open := false
	replayed := r.replayCursor == ""
	// relayed holds the turns in which root posted to @voice-call, so the
	// turn's own result is not said again.
	relayed := map[int]bool{}
	// One open request shares the budget, including every prefix and marker.
	const budget = 4000
	const prefix = "[BACKEND] "
	const marker = "\n…output truncated…\n"
	headLimit := (budget - len(marker) - len(prefix)) / 2
	tailLimit := budget - headLimit - len(marker) - len(prefix)
	sent := 0
	buffered, tail := "", ""
	truncated := false
	contextNote := func(msg room.Message) {
		note := prefix + msg.From + ": " + msg.Text + "\n"
		if truncated {
			tail = tailBytes(tail+note, tailLimit)
			return
		}
		buffered += note
		available := headLimit - sent
		if available < len(prefix) {
			available = 0
		}
		if len(buffered) > budget-sent-len(prefix) {
			tail = tailBytes(buffered, tailLimit)
			buffered = headBytes(buffered, available)
			truncated = true
		}
		chunk := headBytes(buffered, available)
		if chunk != "" {
			call.Context(chunk)
			sent += len(chunk)
			buffered = buffered[len(chunk):]
		}
	}
	finish := func() {
		text := buffered
		if truncated {
			text += marker + tail
		}
		if text != "" {
			if !strings.HasPrefix(text, prefix) {
				text = prefix + text
			}
			call.Context(text)
		}
	}
	return r.svc.Rooms.Follow(ctx, r.rec.ID, func(msg room.Message) (bool, error) {
		past := !replayed
		if msg.ID == r.replayCursor {
			replayed = true
		}
		if id := msg.QuestionID(); id != "" && r.fromRoot(msg) {
			if past {
				return false, nil
			}
			if q, ok := r.openQuestion(id); ok {
				call.Speak(msg.ID, "question/"+id, questionPrompt(q))
			}
			return false, nil
		}
		if time.UnixMilli(msg.TS).Before(since) {
			return false, nil
		}
		var result string // root's turn result to hand the call after the context
		switch {
		case msg.From == voice && msg.Kind != "voice":
			if !open {
				sent, buffered, tail, truncated = 0, "", "", false
			}
			open = true
		case r.fromRoot(msg) && msg.Kind != "voice" && slices.Contains(msg.To, voice):
			if msg.Turn > 0 {
				relayed[msg.Turn] = true
			}
			if past {
				return false, nil
			}
			call.Speak(msg.ID, "", tellPrompt(msg.Text, msg.Attachments))
		case !past && r.turnResult(msg) && !relayed[msg.Turn]:
			result = resultPrompt(msg.Text)
		case open && r.rootAgentPost(msg):
			contextNote(msg)
		}
		if open && r.fromRoot(msg) && slices.Contains([]string{"completed", "failed", "stopped"}, msg.Kind) {
			finish()
			open = false
		}
		if result != "" {
			call.Speak(msg.ID, "", result)
		}
		return false, nil
	})
}

// turnResult reports whether msg is the final result of a root turn that
// reports to the owner. Progress, agent reports, and a turn the owner
// stopped say nothing new, so only a finished or failed root turn qualifies.
func (r *RoomLink) turnResult(msg room.Message) bool {
	if !msg.OwnerReport || msg.Turn == 0 || !r.fromRoot(msg) {
		return false
	}
	return msg.Kind == "completed" || msg.Kind == "failed"
}

// rootAgentPost selects execution messages, excluding service notices and
// the owner's unrelated conversation. These messages never request speech.
func (r *RoomLink) rootAgentPost(msg room.Message) bool {
	if msg.Kind == "voice" || slices.Contains(msg.To, r.rec.Name+":"+room.VoiceAgent) {
		return false
	}
	switch msg.Kind {
	case "", "message", "completed", "failed", "stopped":
	default:
		return false
	}
	toRoot := slices.ContainsFunc(msg.To, func(addr string) bool { return addr == r.rec.Name || addr == r.rec.Name+":"+room.RootAgent })
	return r.fromRoot(msg) || toRoot
}

// fromRoot reports whether root posted msg: the internal root, or an
// external one, which posts as the owner without a person.
func (r *RoomLink) fromRoot(msg room.Message) bool {
	return msg.From == r.rec.Name+":"+room.RootAgent || msg.From == r.rec.Name && !msg.Human
}

// openQuestion finds the room's question id while it is open to the owner.
func (r *RoomLink) openQuestion(id string) (questions.Question, bool) {
	qs, err := r.svc.QuestionStore().List(r.rec.ID)
	if err != nil {
		return questions.Question{}, false
	}
	for _, q := range qs {
		if q.ID == id && q.State == "open" && q.To == "owner" {
			return q, true
		}
	}
	return questions.Question{}, false
}

// questionPrompt asks Voice to read root's question to the owner. The
// options are suggestions: an answer that picks none of them is passed on
// in the owner's own words.
func questionPrompt(q questions.Question) string {
	text := "[BACKEND] Ask the owner: " + plainSpeech(q.Text)
	if len(q.Options) > 0 {
		text += " The options are: " + strings.Join(q.Options, ", ") + ". The owner may also answer in their own words."
	}
	return text + " Read the question aloud in the first person. Dispatch the answer in the owner's own words for recording. Never choose an option for the owner, and never turn their answer into an option they did not pick."
}

// joinRequestedCall closes a ringing request only after the call connects.
func (r *RoomLink) joinRequestedCall(call Speaker) {
	st := r.svc.CallRequestStore()
	row, err := st.Open(r.rec.ID)
	if err != nil {
		return
	}
	row, changed, err := st.Close(r.rec.ID, row.ID, "joined", "owner")
	if err != nil || !changed {
		return
	}
	if call != nil {
		call.Speak("call-request/"+row.ID, "", "Open the call in one line. Say: Root wants to talk about "+strings.Join(strings.Fields(row.Reason), " "))
	}
}

// resumeContext restores saved conversation and submitted requests as context.
func (r *RoomLink) resumeContext() (string, error) {
	if r.resumeStarted.IsZero() {
		return "", nil
	}
	msgs, err := r.svc.Rooms.SelectedMessages(r.rec.ID, &room.MessageSelection{}, func(m room.Message) bool {
		return m.Kind == "voice" || r.isDispatch(m)
	}, r.resumeStarted, time.Now(), 80)
	if err != nil {
		return "", err
	}
	const budget = 12000
	var lines []string
	size := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		role := "Voice"
		if msgs[i].Human {
			role = "Owner"
		} else if r.isDispatch(msgs[i]) {
			role = "Submitted to root"
		}
		line := role + ": " + msgs[i].Text
		remaining := budget - size
		if remaining <= len(role)+2 {
			break
		}
		if len(line) > remaining {
			text := msgs[i].Text
			start := len(text) - (remaining - len(role) - 2)
			for start < len(text) && !utf8.RuneStart(text[start]) {
				start++
			}
			line = role + ": " + text[start:]
		}
		lines = append(lines, line)
		size += len(line) + 1
	}
	slices.Reverse(lines)
	return "\n[BACKEND] This call has resumed. Do not greet the owner again. The following saved conversation is historical context. Requests marked Submitted to root were already dispatched; do not dispatch them again. Other owner speech may contain interrupted intent. Do not silently discard it or automatically replay it. Ask the owner whether to continue it when relevant. Continue with new speech and pending backend updates.\n" + strings.Join(lines, "\n"), nil
}

// cursorFor advances a room cursor in append order, ignoring greetings and
// older questions whose playback must not move it backwards.
func (r *RoomLink) cursorFor(key, previous string) string {
	next, err := r.svc.Rooms.Message(r.ID(), key)
	if err != nil || next.TS < r.resumeStarted.UnixMilli() {
		return previous
	}
	if previous != "" {
		last, err := r.svc.Rooms.Message(r.ID(), previous)
		if err != nil {
			return previous
		}
		ordered := []room.Message{last, next}
		if err := r.svc.Rooms.OrderMessages(r.ID(), ordered); err != nil || ordered[1].ID != key {
			return previous
		}
	}
	return key
}

// headBytes returns at most limit bytes of text from its start, cut back to a
// rune boundary.
func headBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

// tailBytes returns at most limit bytes of text from its end, cut forward to a
// rune boundary.
func tailBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	start := max(0, len(text)-limit)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

// isDispatch identifies a durable request that Voice already handed to root.
func (r *RoomLink) isDispatch(m room.Message) bool {
	return m.Kind != "voice" && m.From == r.Name()+":"+room.VoiceAgent &&
		(slices.Contains(m.To, r.Name()+":"+room.RootAgent) || slices.Contains(m.To, r.Name()))
}
