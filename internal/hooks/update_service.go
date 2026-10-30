package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/updaterservice"
)

// StartUpdate starts the existing detached installer without a shell hook.
func (s Service) StartUpdate(ctx context.Context, event Event, source, toolPath string) (Run, error) {
	r, err := repoid.Resolve(ctx, source)
	if err != nil {
		return Run{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return Run{}, err
	}
	jobs, err := s.installer().Jobs.List()
	if err != nil {
		return Run{}, err
	}
	for _, job := range jobs {
		if job.Hook == updaterservice.RunName && job.Repo == r.ID && job.SHA == event.SHA && job.Status != "finished" {
			return Run{ID: job.ID, Result: ResultPending, LogPath: job.HookLog}, nil
		}
	}
	h := Hook{Name: updaterservice.RunName, Label: "Updates"}
	event.Repo = source
	run, log, err := s.newRun(ctx, r, event, h)
	if err != nil {
		return Run{}, err
	}
	s.emit(event, hookEvent(event, h, run, room.HookRunning, ""))
	config := hookinstall.DefaultConfig(r.Main, exe)
	var dirs []string
	for _, tool := range []string{"go", "npm", "node"} {
		if path, err := exec.LookPath(tool); err == nil {
			dirs = append(dirs, filepath.Dir(path))
		}
	}
	dirs = append(dirs, filepath.SplitList(toolPath)...)
	config.Path = capturedToolPath(append(dirs, filepath.SplitList(os.Getenv("PATH"))...), s.dataDir())
	job := hookinstall.Job{ID: run.ID, SHA: event.SHA, Repo: r.ID, Hook: h.Name, Room: event.Room, Session: event.Session, HookLog: run.LogPath, Config: config}
	err = s.installer().Start(ctx, job, log)
	if closeErr := log.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		run.Result = ResultError
		run.FinishedAt = time.Now().UTC()
		if _, saveErr := s.saveRun(ctx, run); saveErr != nil {
			return run, fmt.Errorf("start update: %v; save result: %w", err, saveErr)
		}
		s.alert(r, event, h, run, room.HookError, "install launch", "The update could not start.")
	}
	return run, err
}
