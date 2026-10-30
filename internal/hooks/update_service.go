package hooks

import (
	"context"
	"fmt"
	"os"
	"time"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/updaterservice"
)

// StartUpdate starts the existing detached installer without a shell hook.
func (s Service) StartUpdate(ctx context.Context, event Event, source string) (Run, error) {
	r, err := repoid.Resolve(ctx, source)
	if err != nil {
		return Run{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return Run{}, err
	}
	h := Hook{Name: updaterservice.RunName, Label: "Updates"}
	event.Repo = source
	run, log, err := s.newRun(ctx, r, event, h)
	if err != nil {
		return Run{}, err
	}
	s.emit(event, hookEvent(event, h, run, room.HookRunning, ""))
	job := hookinstall.Job{ID: run.ID, SHA: event.SHA, Repo: r.ID, Hook: h.Name, Room: event.Room, Session: event.Session, HookLog: run.LogPath, Config: hookinstall.DefaultConfig(r.Main, exe)}
	err = s.installer().Start(ctx, job, log)
	if closeErr := log.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		run.Result = "error"
		run.FinishedAt = time.Now().UTC()
		if _, saveErr := s.saveRun(ctx, run); saveErr != nil {
			return run, fmt.Errorf("start update: %v; save result: %w", err, saveErr)
		}
		s.alert(r, event, h, run, room.HookError, "install launch", "The update could not start.")
	}
	return run, err
}
