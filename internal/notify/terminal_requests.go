package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/subagent"
)

// TerminalRequestInput is a work session's request to run a command in an
// owner terminal. An empty Argv asks for a plain login shell.
type TerminalRequestInput struct {
	Room   string   `json:"room"`
	Argv   []string `json:"argv"`
	Cwd    string   `json:"cwd"`
	Reason string   `json:"reason"`
	Claim  Claim    `json:"claim"`
}

var errTerminalFiler = errors.New("only a work session files a terminal request; the owner opens a terminal directly")

// nextHolder is the session that decides a request after from: the nearest
// open ancestor, then the room's root. It is empty when the owner is next.
func (s *Service) nextHolder(from subagent.Info) (string, error) {
	if from.Role == subagent.RoleRoot {
		return "", nil
	}
	for cur := from; cur.ParentSession != ""; {
		parent, err := s.sessionStore().Info(cur.ParentSession)
		if err != nil {
			return "", err
		}
		if parent.Status != subagent.StatusClosed {
			return parent.ID, nil
		}
		cur = parent
	}
	root, ok, err := s.Sessions.RootSession(from.RoomID)
	if err != nil || !ok || root.ID == from.ID {
		return "", err
	}
	return root.ID, nil
}

// FileTerminalRequest records a request, tells its first holder, and withdraws
// the session's older request.
func (s *Service) FileTerminalRequest(ctx context.Context, snd Sender, req TerminalRequestInput) (questions.Question, error) {
	if snd.Kind != SenderSession {
		return questions.Question{}, errTerminalFiler
	}
	meta, err := s.sessionStore().ReadMeta(snd.Session.ID)
	if err != nil {
		return questions.Question{}, err
	}
	if meta.ServiceContract().Active() {
		return questions.Question{}, subagent.ErrServiceOutput
	}
	rec, err := s.QuestionRoom(snd, req.Room)
	if err != nil {
		return questions.Question{}, err
	}
	holder, err := s.nextHolder(snd.Session)
	if err != nil {
		return questions.Question{}, err
	}
	t := &questions.TerminalRequest{Argv: req.Argv, Cwd: req.Cwd, Reason: req.Reason, Stage: questions.StageWaitingOwner, Holder: holder}
	if holder != "" {
		t.Stage = questions.StageWaitingSession
	}
	q := questions.Question{Room: rec.ID, Asker: snd.Session.ID, AskerKind: questions.AskerSession, To: "owner", Text: req.Reason, Kind: questions.KindTerminal, Terminal: t, AskedTurn: snd.Session.CurrentTurn}
	var deliveryErr error
	q, err = s.QuestionStore().Ask(q, func(q questions.Question) (string, error) {
		msg, err := s.postTerminalRequest(ctx, rec, q, snd.Session.Address())
		deliveryErr = err
		return msg.ID, nil
	})
	if err != nil {
		return q, err
	}
	// One open request per session: a new one replaces the older.
	open, err := s.QuestionStore().OpenAskedBy(rec.ID, q.Asker)
	if err != nil {
		return q, err
	}
	for _, old := range open {
		if old.IsTerminal() && old.ID != q.ID {
			if err := s.withdrawTerminal(rec, old, snd, "replaced"); err != nil {
				return q, err
			}
		}
	}
	s.noteQuestions(ctx, rec.ID)
	return q, deliveryErr
}

// postTerminalRequest tells the holder of q, or the owner, that the request
// waits. A holding session wakes; the owner's copy wakes no session.
func (s *Service) postTerminalRequest(ctx context.Context, rec room.Record, q questions.Question, asker string) (room.Message, error) {
	t := q.Terminal
	ev := room.TerminalRequestEvent{RequestID: q.ID, Asker: asker, Argv: t.Argv, Cwd: t.Cwd, Reason: t.Reason}
	in := room.PostEventInput{RoomID: rec.ID, Event: ev, To: []string{rec.Name}, Via: []string{"@root"}, NoWake: true}
	var target recipient
	if t.Stage == questions.StageWaitingSession {
		info, err := s.sessionStore().Info(t.Holder)
		if err != nil {
			return room.Message{}, err
		}
		target = recipient{kind: toSession, addr: info.Address(), rec: rec, session: info}
		ev.Holder = info.Address()
		ev.Detail = fmt.Sprintf("You hold this request. Command: %s\nFolder: %s\nPass it up with: dyna request approve %s\nRefuse it with: dyna request deny %s --reason \"<why>\"", commandLine(t.Argv), t.Cwd, q.ID, q.ID)
		in = room.PostEventInput{RoomID: rec.ID, Event: ev, To: []string{target.addr}, Via: []string{"@" + target.addr}, SessionIDs: map[string]string{target.addr: info.ID}}
	}
	msg, err := s.Rooms.PostEventInput(in)
	if err != nil || msg.ID == "" || target.kind != toSession {
		return msg, err
	}
	if d := s.deliver(msg, target); d.Error != "" {
		return msg, fmt.Errorf("request %s is saved; delivery to its holder failed: %s", q.ID, d.Error)
	}
	return msg, nil
}

func commandLine(argv []string) string {
	if len(argv) == 0 {
		return "(a plain login shell)"
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellquote.Quote(arg)
	}
	return strings.Join(quoted, " ")
}

// ApproveTerminalRequest passes a request the session holds up the chain. The
// root's approval makes it visible to the owner.
func (s *Service) ApproveTerminalRequest(ctx context.Context, snd Sender, roomRef, id string) error {
	if snd.Kind != SenderSession {
		return errors.New("only the session that holds a request approves it")
	}
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	next, err := s.nextHolder(snd.Session)
	if err != nil {
		return err
	}
	q, err := s.QuestionStore().ApproveTerminal(rec.ID, id, snd.Session.ID, next)
	if err != nil {
		return err
	}
	asker := q.Asker
	if info, err := s.sessionStore().Info(q.Asker); err == nil {
		asker = info.Address()
	}
	_, err = s.postTerminalRequest(ctx, rec, q, asker)
	s.noteQuestions(ctx, rec.ID)
	return err
}

// DenyTerminalRequest ends a request the session holds and tells the filer why.
func (s *Service) DenyTerminalRequest(ctx context.Context, snd Sender, roomRef, id, reason string) error {
	if snd.Kind != SenderSession {
		return errors.New("only the session that holds a request denies it")
	}
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	if _, err := s.QuestionStore().DenyTerminal(rec.ID, id, snd.Session.ID, reason); err != nil {
		return err
	}
	s.noteQuestions(ctx, rec.ID)
	return s.DeliverAnswers(ctx, rec.ID)
}

// withdrawTerminal ends an open request and posts the quiet end note.
func (s *Service) withdrawTerminal(rec room.Record, q questions.Question, snd Sender, reason string) error {
	if _, err := s.QuestionStore().Withdraw(rec.ID, q.ID, questionIdentity(snd), snd.Human, reason); err != nil {
		return err
	}
	_, err := s.Rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: room.TerminalDoneEvent{RequestID: q.ID, Verdict: room.TerminalWithdrawn, Note: reason}, To: []string{rec.Name}, Via: []string{"@root"}, NoWake: true})
	return err
}

// OwnerTerminalRequest returns a request that waits for the owner. A request
// at any other stage is not the owner's to run.
func (s *Service) OwnerTerminalRequest(roomRef, id string) (room.Record, questions.Question, error) {
	rec, err := s.Rooms.Resolve(roomRef)
	if err != nil {
		return rec, questions.Question{}, err
	}
	list, err := s.QuestionStore().List(rec.ID)
	if err != nil {
		return rec, questions.Question{}, err
	}
	for _, q := range list {
		if q.ID != id || !q.IsTerminal() {
			continue
		}
		if q.State != "open" || q.Terminal == nil || q.Terminal.Stage != questions.StageWaitingOwner {
			return rec, q, fmt.Errorf("request %s is %s, not waiting for the owner", id, q.TerminalStage())
		}
		return rec, q, nil
	}
	return rec, questions.Question{}, fmt.Errorf("no terminal request %s in this room", id)
}

// ReportTerminalRequest records the owner's result and wakes the filer.
func (s *Service) ReportTerminalRequest(ctx context.Context, roomRef, id string, r questions.TerminalReport) error {
	rec, _, err := s.OwnerTerminalRequest(roomRef, id)
	if err != nil {
		return err
	}
	if _, err := s.QuestionStore().ReportTerminal(rec.ID, id, r); err != nil {
		return err
	}
	s.noteQuestions(ctx, rec.ID)
	return s.DeliverAnswers(ctx, rec.ID)
}

// terminalVerdict maps an ended stage to the verdict the event carries. It is
// the one place the store's stages meet the event's verdicts; a stage that
// has not ended has none.
func terminalVerdict(stage questions.TerminalStage) (room.TerminalVerdict, bool) {
	switch stage {
	case questions.StageDone:
		return room.TerminalDone, true
	case questions.StageFailed:
		return room.TerminalFailed, true
	case questions.StageDeclined:
		return room.TerminalDeclined, true
	case questions.StageDenied:
		return room.TerminalDenied, true
	case questions.StageWithdrawn:
		return room.TerminalWithdrawn, true
	case questions.StageWaitingSession, questions.StageWaitingOwner:
		return "", false
	}
	return "", false
}

// terminalDone is the typed result the filer reads.
func terminalDone(q questions.Question, by string) room.TerminalDoneEvent {
	verdict, _ := terminalVerdict(q.TerminalStage())
	ev := room.TerminalDoneEvent{RequestID: q.ID, Verdict: verdict}
	if q.Answer != nil {
		ev.Note = q.Answer.Text
	}
	t := q.Terminal
	if ev.Verdict == room.TerminalDenied {
		ev.Detail = "Denied by " + by + ". It did not reach the owner."
	}
	if t.ExitCode != nil {
		ev.Exit = &room.TerminalExit{Code: *t.ExitCode}
		ev.Detail = fmt.Sprintf("Exit code %d.", *t.ExitCode)
	}
	if t.SharedLines != "" {
		ev.SharedLines = strings.Split(t.SharedLines, "\n")
	}
	return ev
}

// WithdrawRequest withdraws a question or a terminal request. A terminal
// request is withdrawn only by its filer; a question keeps its own rules.
func (s *Service) WithdrawRequest(snd Sender, roomRef, id, reason string) error {
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	list, err := s.QuestionStore().List(rec.ID)
	if err != nil {
		return err
	}
	for _, q := range list {
		if q.ID != id || !q.IsTerminal() {
			continue
		}
		if q.Asker != questionIdentity(snd) {
			return fmt.Errorf("only the session that filed request %s withdraws it", id)
		}
		if err := s.withdrawTerminal(rec, q, snd, reason); err != nil {
			return err
		}
		s.noteQuestions(context.Background(), rec.ID)
		return nil
	}
	return s.WithdrawQuestion(snd, roomRef, id, reason)
}

// deliverTerminalResult posts the typed result to the filer and marks the
// answer delivered. A closed filer's result goes to the owner.
func (s *Service) deliverTerminalResult(rec room.Record, q questions.Question, target recipient, by string, closed bool) error {
	denier := by
	if info, err := s.sessionStore().Info(q.Answer.By); err == nil {
		denier = info.EffectiveName()
	}
	in := room.PostEventInput{RoomID: rec.ID, Event: terminalDone(q, denier), ID: questions.PostID(q.Room, q.ID, "answer"), To: []string{target.addr}, Via: []string{"@" + target.addr}, NoWake: closed}
	if target.kind == toSession {
		in.SessionIDs = map[string]string{target.addr: target.session.ID}
	}
	msg, err := s.Rooms.PostEventInput(in)
	if err != nil {
		return err
	}
	if msg.ID != "" && !closed {
		if d := s.deliver(msg, target); d.Error != "" {
			return fmt.Errorf("answer %s is saved; delivery failed: %s", q.ID, d.Error)
		}
	}
	return s.QuestionStore().MarkDelivered(rec.ID, q.ID)
}

// RepostTerminalRequests tells the new holder of each moved request, or the
// owner, so a request does not stall when its holder closes.
func (s *Service) RepostTerminalRequests(ctx context.Context, moved []questions.Question) error {
	var errs []error
	for _, q := range moved {
		rec, err := s.Rooms.Resolve(q.Room)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		asker := q.Asker
		if info, err := s.sessionStore().Info(q.Asker); err == nil {
			asker = info.Address()
		}
		if _, err := s.postTerminalRequest(ctx, rec, q, asker); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
