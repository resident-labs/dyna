package notify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func (s *Service) QuestionStore() *questions.Store {
	root := s.QuestionRoot
	if root == "" {
		root = filepath.Join(filepath.Dir(s.sessionStore().Root), "questions")
	}
	s.questionMu.Lock()
	defer s.questionMu.Unlock()
	if s.questions == nil || s.questions.Root != root {
		s.questions = &questions.Store{Root: root}
	}
	return s.questions
}

func questionIdentity(snd Sender) string {
	switch snd.Kind {
	case SenderUnverified:
		return "unverified"
	case SenderSession:
		return snd.Session.ID
	case SenderWorker:
		return fmt.Sprintf("run:%s/agent:%d", snd.RunID, snd.Agent)
	case SenderScript:
		return "run:" + snd.RunID
	default:
		return "owner"
	}
}

// QuestionRoom uses the sender's room, or an explicit room for the owner's CLI.
func (s *Service) QuestionRoom(snd Sender, ref string) (room.Record, error) {
	if snd.Room != "" {
		rec, err := s.Rooms.Resolve(snd.Room)
		if err != nil {
			return room.Record{}, err
		}
		if ref != "" {
			want, err := s.Rooms.Resolve(ref)
			if err != nil {
				return room.Record{}, err
			}
			if want.ID != rec.ID {
				return room.Record{}, fmt.Errorf("questions must stay in your room")
			}
		}
		return rec, nil
	}
	if ref != "" {
		return s.Rooms.Resolve(ref)
	}
	return s.Rooms.CurrentRepoRoom()
}

// ErrAskAgent rejects questions to agents. Agents can answer posts, so a
// question between agents is a post, and questions wait only for the owner.
var ErrAskAgent = errors.New("Ask agents with dyna post; dyna ask is for the owner.")

func (s *Service) Ask(ctx context.Context, snd Sender, roomRef, to, text, meanwhile string, options []string, files []File) (questions.Question, error) {
	if snd.Kind == SenderSession {
		meta, err := s.sessionStore().ReadMeta(snd.Session.ID)
		if err != nil {
			return questions.Question{}, err
		}
		if meta.ServiceContract().Active() {
			return questions.Question{}, subagent.ErrServiceOutput
		}
		snd.Session.Role = meta.Role
	}
	if to != "" && to != "@root" && to != "owner" {
		return questions.Question{}, ErrAskAgent
	}
	if err := authority.Check("ask", senderActor(snd), nil); err != nil {
		return questions.Question{}, err
	}
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return questions.Question{}, err
	}
	snd.Room = rec.Name
	q := questions.Question{Room: rec.ID, Asker: questionIdentity(snd), Human: snd.Human, To: "owner", Text: text, Meanwhile: meanwhile, Options: options}
	if snd.Kind == SenderSession {
		q.AskedTurn = snd.Session.CurrentTurn
	}
	var deliveryErr error
	q, err = s.QuestionStore().Ask(q, func(q questions.Question) (string, error) {
		body := fmt.Sprintf("[QUESTION %s] %s\nWhile waiting: %s\nAnswer: dyna ask answer %s \"your answer\"", q.ID, q.Text, q.Meanwhile, q.ID)
		if len(q.Options) > 0 {
			body += "\nOptions: " + strings.Join(q.Options, ", ")
		}
		result, err := s.Post(ctx, PostInput{Sender: snd, Mentions: []string{"@" + rec.Name}, Kind: "message", Text: body, Files: files})
		if result.Message.ID != "" {
			deliveryErr = err
			return result.Message.ID, nil
		}
		return "", err
	})
	if err != nil {
		return q, err
	}
	s.noteQuestions(ctx, rec.ID)
	return q, deliveryErr
}

// noteQuestions queues an instruction note for the room's sessions, so root's
// Open questions section reaches a running turn as a drift note.
func (s *Service) noteQuestions(ctx context.Context, roomID string) {
	if s.Sessions != nil {
		s.Sessions.NoteRoomInstructions(ctx, roomID)
	}
}
func (s *Service) AnswerQuestion(ctx context.Context, snd Sender, roomRef, id, choice, text string) error {
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	_, err = s.QuestionStore().Answer(rec.ID, id, questions.Answer{By: questionIdentity(snd), Human: snd.Human, Choice: choice, Text: text})
	if err != nil {
		qs, readErr := s.QuestionStore().List(rec.ID)
		retry := false
		if readErr == nil {
			for _, q := range qs {
				if q.ID == id && q.State == "answered" && q.Answer != nil && q.Answer.By == questionIdentity(snd) && q.Answer.Choice == choice && q.Answer.Text == strings.TrimSpace(text) {
					retry = true
				}
			}
		}
		if !retry {
			return err
		}
	}
	s.noteQuestions(ctx, rec.ID)
	return s.DeliverAnswers(ctx, rec.ID)
}

// DeliverAnswers retries stable answer posts without changing a stored answer.
func (s *Service) DeliverAnswers(ctx context.Context, roomID string) error {
	list, err := s.QuestionStore().List(roomID)
	if err != nil {
		return err
	}
	rec, err := s.Rooms.Resolve(roomID)
	if err != nil {
		return err
	}
	var errs []error
	for _, q := range list {
		if q.State != "answered" || q.AnswerDelivered || q.Answer == nil {
			continue
		}
		a := q.Answer
		from, by := rec.Name, room.OwnerLabel(a.Human)
		if a.By != "owner" {
			from, by = a.By, a.By
			if info, err := s.sessionStore().Info(a.By); err == nil {
				from, by = info.Address(), info.EffectiveName()
			}
		}
		target := recipient{kind: toOwner, addr: rec.Name, rec: rec}
		closed := false
		if q.Asker != "owner" {
			if strings.HasPrefix(q.Asker, "run:") {
				resolved, err := s.resolve(Sender{Kind: SenderOwner, Room: rec.Name}, []string{"@" + q.Asker})
				if err != nil {
					errs = append(errs, err)
					continue
				}
				target = resolved.recipients[0]
			} else {
				info, err := s.sessionStore().Info(q.Asker)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				if info.RoomID != rec.ID {
					errs = append(errs, fmt.Errorf("asker is in another room"))
					continue
				}
				target = recipient{kind: toSession, addr: info.Address(), rec: rec, session: info}
				closed = info.Status == subagent.StatusClosed
				if closed {
					target.addr, target.kind = rec.Name, toOwner
				}
			}
		}
		answer := a.Choice
		if a.Text != "" {
			if answer != "" {
				answer += ". "
			}
			answer += a.Text
		}
		body := fmt.Sprintf("[ANSWER %s] Your question: %s\nAnswer: %s — %s", q.ID, q.Text, answer, by)
		// Integration posts use stable content and the same delivery gate as
		// ordinary posts. The stored asker ID prevents a reused name taking
		// an old session's answer. Self-answers need no additional wake.
		var sessionIDs map[string]string
		if target.kind == toSession {
			sessionIDs = map[string]string{target.addr: target.session.ID}
		}
		msg, err := s.Rooms.Post(rec.ID, room.PostInput{
			SessionIDs: sessionIDs, Wake: !closed && q.Asker != a.By, ID: questions.PostID(q.Room, q.ID, "answer"), From: from, Human: a.Human,
			Kind: "message", To: []string{target.addr}, Via: []string{"@" + target.addr}, Text: body,
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !closed && q.Asker != a.By {
			if delivery := s.deliver(msg, target); delivery.Error != "" {
				errs = append(errs, fmt.Errorf("answer %s is saved; delivery failed: %s", q.ID, delivery.Error))
				continue
			}
		}
		if err = s.QuestionStore().MarkDelivered(rec.ID, q.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (s *Service) WithdrawQuestion(snd Sender, roomRef, id, reason string) error {
	if err := authority.Check("ask withdraw", senderActor(snd), nil); err != nil {
		return err
	}
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	q, err := s.QuestionStore().Withdraw(rec.ID, id, questionIdentity(snd), snd.Human, reason)
	if err != nil {
		return err
	}
	s.noteQuestions(context.Background(), rec.ID)
	// TODO(legacy): State-only questions have no post to reply to.
	if q.Post == "" {
		return nil
	}
	post, err := s.Rooms.Message(rec.ID, q.Post)
	if err != nil {
		return err
	}
	from := questionSenderAddress(snd, rec.Name)
	by := from
	if snd.Human {
		by = rec.Name
	}
	event := room.QuestionEvent{QuestionID: q.ID, Post: q.Post, By: by, Reason: reason}
	_, err = s.Rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: event, ID: questions.PostID(q.Room, q.ID, "withdrawn"), To: questionParticipants(post, from), ReplyTo: q.Post})
	return err
}

// Withdrawal records stay with the original participants, like a normal reply.
// When the sender was the only participant, as when the owner withdraws their
// own question, the record goes to the sender, like a post to @root alone.
func questionParticipants(post room.Message, from string) []string {
	out := []string{}
	seen := map[string]bool{from: true}
	for _, addr := range append(append([]string{}, post.To...), post.From) {
		if !seen[addr] {
			out = append(out, addr)
			seen[addr] = true
		}
	}
	if len(out) == 0 {
		return []string{from}
	}
	return out
}
func questionSenderAddress(snd Sender, owner string) string {
	switch snd.Kind {
	case SenderUnverified:
		return "unverified"
	case SenderSession:
		return snd.Session.Address()
	case SenderWorker:
		return fmt.Sprintf("run:%s/agent:%d", snd.RunID, snd.Agent)
	case SenderScript:
		return "run:" + snd.RunID
	default:
		return owner
	}
}
