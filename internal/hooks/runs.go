package hooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
)

func (s Service) execute() processguard.Executor {
	if s.Exec != nil {
		return s.Exec
	}
	return processguard.Execute
}

func (s Service) newRun(ctx context.Context, r repoid.Repo, e Event, h Hook) (Run, *os.File, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Run{}, nil, err
	}
	logs := filepath.Join(s.dataDir(), "hooks", "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		return Run{}, nil, err
	}
	id := hex.EncodeToString(token[:])
	log, err := os.OpenFile(filepath.Join(logs, "hook-"+h.Name+"-"+id+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Run{}, nil, err
	}
	run := Run{RunnerSession: e.Session, ID: id, Repo: r.ID, Main: r.Main, Room: e.Room, Event: e.Event, Label: h.Label, Name: "hook-" + h.Name, Command: h.Command, Result: "pending", LogPath: log.Name(), StartedAt: time.Now().UTC()}
	if _, err = s.saveRun(ctx, run); err != nil {
		log.Close()
		return Run{}, nil, err
	}
	return run, log, nil
}
