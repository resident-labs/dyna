package engine

import (
	"context"
	"errors"
	"log"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/websvc"
)

// termRequestRun starts the owner terminal for a request that waits for the
// owner. argv is the command the owner edited, or nil to run the filed one.
// A second call returns the terminal while it is alive.
func (s services) termRequestRun(ctx context.Context, roomRef, id string, argv []string, cols, rows uint16) (terminalsvc.Info, error) {
	rec, q, err := s.rooms.Notify.OwnerTerminalRequest(roomRef, id)
	if err != nil {
		return terminalsvc.Info{}, err
	}
	t := q.Terminal
	if t.TermID != "" {
		if info, err := s.terminals.Info(t.TermID); err == nil && info.Alive {
			return info, nil
		}
	}
	if argv == nil {
		argv = t.Argv
	}
	if err := questions.CheckArgv(argv); err != nil {
		return terminalsvc.Info{}, err
	}
	info, err := s.terminals.OpenRequest(rec.ID, q.Asker, t.Cwd, argv, cols, rows)
	if err != nil {
		return info, err
	}
	if _, err := s.rooms.Notify.QuestionStore().SetTerminal(rec.ID, id, argv, t.TermID, info.ID); err != nil {
		_ = s.terminals.Close(info.ID)
		return terminalsvc.Info{}, err
	}
	// The engine sees the exit, so the filer hears back even if the owner
	// closes the tab or the browser first.
	if err := s.terminals.AfterExit(info.ID, func(code int) {
		if err := s.rooms.Notify.ReportTerminalExit(context.Background(), rec.ID, id, code); err != nil {
			log.Printf("terminal request %s: report on exit: %v", id, err)
		}
	}); err != nil {
		log.Printf("terminal request %s: watch exit: %v", id, err)
	}
	return info, nil
}

// termRequestPreview returns the lines the owner could share, also after the
// exit report went out.
func (s services) termRequestPreview(roomRef, id string) (string, error) {
	_, q, err := s.rooms.Notify.OwnedTerminalRequest(roomRef, id)
	if err != nil {
		return "", err
	}
	if q.Terminal.TermID == "" {
		return "", errors.New("the request has no terminal yet")
	}
	return s.terminals.Tail(q.Terminal.TermID, terminalsvc.RequestLines)
}

// termRequestReport records the owner's verdict. The screen lines go to the
// filer only when shareLines is true.
func (s services) termRequestReport(ctx context.Context, roomRef, id string, verdict questions.TerminalVerdict, note string, shareLines bool) error {
	_, q, err := s.rooms.Notify.OwnerTerminalRequest(roomRef, id)
	if err != nil {
		return err
	}
	report := questions.TerminalReport{Verdict: verdict, Note: note, Human: true}
	if termID := q.Terminal.TermID; termID != "" && verdict != questions.VerdictDeclined {
		if report, err = s.terminalResult(report, termID, shareLines); err != nil {
			return err
		}
	}
	return s.rooms.Notify.ReportTerminalRequest(ctx, roomRef, id, report)
}

// terminalResult adds what the request's terminal shows: its exit code once it
// exited, and its last lines only when the owner chose to share them.
func (s services) terminalResult(report questions.TerminalReport, termID string, shareLines bool) (questions.TerminalReport, error) {
	if info, err := s.terminals.Info(termID); err == nil && info.Exited {
		report.ExitCode = &info.ExitCode
	}
	if shareLines {
		lines, err := s.terminals.Tail(termID, terminalsvc.RequestLines)
		if err != nil {
			return report, err
		}
		report.SharedLines = strings.TrimSpace(lines)
	}
	return report, nil
}

func (s services) fileTerminalRequest(ctx context.Context, req notify.TerminalRequestInput) (questions.Question, error) {
	caller, local := websvc.CallerFromContext(ctx)
	snd, err := postSender(caller, local, authority.Of(ctx), req.Claim, enforceCallers, log.Printf, s.rooms.Notify.SenderOf)
	if err != nil {
		return questions.Question{}, err
	}
	return s.rooms.Notify.FileTerminalRequest(ctx, snd, req)
}

func (s services) approveRequest(ctx context.Context, ref, id string) error {
	snd, err := s.questionSender(ctx)
	if err != nil {
		return err
	}
	return s.rooms.Notify.ApproveTerminalRequest(ctx, snd, ref, id)
}

func (s services) denyRequest(ctx context.Context, ref, id, reason string) error {
	snd, err := s.questionSender(ctx)
	if err != nil {
		return err
	}
	return s.rooms.Notify.DenyTerminalRequest(ctx, snd, ref, id, reason)
}

func (s services) withdrawRequest(ctx context.Context, ref, id, reason string) error {
	snd, err := s.questionSender(ctx)
	if err != nil {
		return err
	}
	return s.rooms.Notify.WithdrawRequest(snd, ref, id, reason)
}
