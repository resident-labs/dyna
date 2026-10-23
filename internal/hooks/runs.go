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
	// Event is the event that started the run, and Main is the repository's
	// main checkout at that moment. A run that an engine leaves pending has no
	// hook left to ask, so it keeps what its result card needs: the event, the
	// label, the checkout to post to and the room the event belongs to. Room is
	// empty when the repository's own room is the place to post.
	Event string `json:"event,omitempty"`
	Label string `json:"label,omitempty"`
	Main  string `json:"main,omitempty"`
	Room  string `json:"room,omitempty"`
}

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
