package voice

import (
	"context"
	"errors"
	"regexp"
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

// Ask posts the owner's spoken request, in Voice's words, from Voice to
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

// reportHeader and reportPointer are the parts notify.CompletionText adds
// around a turn's result: "<address> turn <n> <kind>. Result:" and the
// "Full output: …" line.
var (
	reportHeader  = regexp.MustCompile(`^\S+ turn \d+ \w+\.(?: Result:)?\s*`)
	reportPointer = regexp.MustCompile(`\s*Full output: dyna subagent output \S+\s*$`)
)

// resultText is a turn report's result without its header and pointer. A
// post without the header stays as it is.
func resultText(text string) string {
	result := reportPointer.ReplaceAllString(reportHeader.ReplaceAllString(text, ""), "")
	if result == "" {
		return text
	}
	return result
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
	Progress(task string, started time.Time, text string)
	EndProgress(task string)
}

// Watch hands the call what root says to the owner: each post to
// @voice-call from since on, and each of root's questions to the owner
// while it is open, the oldest first. A question's topic is its ID, so a
// newer post of the same question replaces one not yet said. While a
// request Voice dispatched is open, each post between root and its agents
// is that work's progress; root's answer to the call, or the end of root's
// turn, closes the requests. It runs until ctx ends.
func (r *RoomLink) Watch(ctx context.Context, since time.Time, call Speaker) error {
	voice := r.rec.Name + ":" + room.VoiceAgent
	open := map[string]time.Time{} // requests Voice dispatched, and when
	closeAll := func() {
		for task := range open {
			call.EndProgress(task)
		}
		clear(open)
	}
	return r.svc.Rooms.Follow(ctx, r.rec.ID, func(msg room.Message) (bool, error) {
		at := time.UnixMilli(msg.TS)
		if id := msg.QuestionID(); id != "" && r.fromRoot(msg) {
			if q, ok := r.openQuestion(id); ok {
				call.Speak(msg.ID, "question/"+id, questionPrompt(q))
			}
			return false, nil
		}
		if at.Before(since) {
			return false, nil
		}
		switch {
		case msg.From == voice && msg.Kind != "voice":
			open[msg.ID] = at
		// Only root may post to Voice; the call's own transcript is kind voice.
		case msg.Kind != "voice" && slices.Contains(msg.To, voice):
			call.Speak(msg.ID, "", tellPrompt(msg.Text, msg.Attachments))
			closeAll()
		case msg.From == r.rec.Name+":"+room.RootAgent && slices.Contains([]string{"completed", "failed", "stopped"}, msg.Kind):
			closeAll()
		case r.rootAgentPost(msg):
			for task, started := range open {
				call.Progress(task, started, resultText(msg.Text))
			}
		}
		return false, nil
	})
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
	text := "Ask the owner: " + q.Text
	if len(q.Options) > 0 {
		text += " The options are: " + strings.Join(q.Options, ", ") + ". The owner may also answer in their own words."
	}
	return text + " Read the question aloud in the first person, and give the owner's answer with answer_question, in the owner's own words, with question_id " + q.ID + ". Never say the ID aloud. Never choose an option for the owner, and never turn their answer into an option they did not pick."
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
