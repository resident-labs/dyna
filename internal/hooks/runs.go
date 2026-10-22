package hooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/processguard"
)

// Run records one repository hook attempt independently of test evidence.
type Run struct {
	RunnerSession string    `json:"runnerSession,omitempty"`
	ID            string    `json:"id"`
	Repo          string    `json:"repo"`
	Name          string    `json:"name"`
	Command       string    `json:"command"`
	Result        string    `json:"result"`
	LogPath       string    `json:"logPath"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt,omitempty"`
}

func (s Service) execute() processguard.Executor {
	if s.Exec != nil {
		return s.Exec
	}
	return processguard.Execute
}

func (s Service) newRun(ctx context.Context, repo string, h Hook, session string) (Run, *os.File, error) {
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
	run := Run{RunnerSession: session, ID: id, Repo: repo, Name: "hook-" + h.Name, Command: h.Command, Result: "pending", LogPath: log.Name(), StartedAt: time.Now().UTC()}
	if _, err = s.saveRun(ctx, run); err != nil {
		log.Close()
		return Run{}, nil, err
	}
	return run, log, nil
}
