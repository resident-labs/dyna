package voice

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

// RoomLink posts the owner's requests to root and the call's transcript.
type RoomLink struct {
	svc *notify.Service
	rec room.Record
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
// @voice-call from since on, and each of root's questions to the owner
// while it is open, the oldest first. A question's topic is its ID, so a
// newer post of the same question replaces one not yet said. While root
// works on a dispatched request, its agent traffic is silent context. Only
// explicit call posts and owner questions request speech. It runs until ctx ends.
func (r *RoomLink) Watch(ctx context.Context, since time.Time, call Speaker) error {
	voice := r.rec.Name + ":" + room.VoiceAgent
	open := false
	return r.svc.Rooms.Follow(ctx, r.rec.ID, func(msg room.Message) (bool, error) {
		if id := msg.QuestionID(); id != "" && r.fromRoot(msg) {
			if q, ok := r.openQuestion(id); ok {
				call.Speak(msg.ID, "question/"+id, questionPrompt(q))
			}
			return false, nil
		}
		if time.UnixMilli(msg.TS).Before(since) {
			return false, nil
		}
		switch {
		case msg.From == voice && msg.Kind != "voice":
			open = true
		case r.fromRoot(msg) && msg.Kind != "voice" && slices.Contains(msg.To, voice):
			call.Speak(msg.ID, "", tellPrompt(msg.Text, msg.Attachments))
		case r.fromRoot(msg) && slices.Contains([]string{"completed", "failed", "stopped"}, msg.Kind):
			open = false
		case open && r.rootAgentPost(msg):
			call.Context("[BACKEND] " + msg.From + ": " + msg.Text)
		}
		return false, nil
	})
}

// rootAgentPost selects execution messages, excluding service notices and
// the owner's unrelated conversation. These messages never request speech.
func (r *RoomLink) rootAgentPost(msg room.Message) bool {
	switch msg.Kind {
	case "", "message", "completed", "failed", "stopped":
	default:
		return false
	}
	agent := func(addr string) bool {
		name, ok := strings.CutPrefix(addr, r.rec.Name+":")
		return ok && name != "" && name != room.RootAgent && name != room.VoiceAgent
	}
	toRoot := slices.ContainsFunc(msg.To, func(addr string) bool { return addr == r.rec.Name || addr == r.rec.Name+":"+room.RootAgent })
	return r.fromRoot(msg) && slices.ContainsFunc(msg.To, agent) || agent(msg.From) && toRoot
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
	text := "[BACKEND] Ask the owner: " + q.Text
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
