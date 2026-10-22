// Package hookinstall implements the opt-in Dyna install hook. A durable job
// connects a detached systemd unit to the hook run that started it.
package hookinstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/store"
	"dyna-agent/internal/userservices"
)

type Config struct {
	Main       string `json:"main"`
	Build      string `json:"build"`
	Bin        string `json:"bin"`
	State      string `json:"state"`
	Lock       string `json:"lock"`
	Executable string `json:"executable"`
	Path       string `json:"path"`
	Units      string `json:"units,omitempty"`
}

func DefaultConfig(main, exe string) Config {
	home, _ := os.UserHomeDir()
	return Config{Main: main, Build: main + "-autoinstall", Bin: filepath.Join(home, ".local", "bin"), State: filepath.Join(home, ".local", "share", "dyna-sync", "installed-commit"), Lock: "/tmp/dyna-agent-install.lock", Executable: exe, Path: os.Getenv("PATH"), Units: userservices.Dir()}
}

type Warning struct {
	Cause string `json:"cause"`
	Text  string `json:"text"`
}
type Job struct {
	ID         string    `json:"id"`
	Unit       string    `json:"unit"`
	Repo       string    `json:"repo"`
	Hook       string    `json:"hook"`
	Room       string    `json:"room,omitempty"`
	Session    string    `json:"session,omitempty"`
	SHA        string    `json:"sha"`
	Config     Config    `json:"config"`
	HookLog    string    `json:"hookLog"`
	WorkerLog  string    `json:"workerLog,omitempty"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Cause      string    `json:"cause,omitempty"`
	Warnings   []Warning `json:"warnings,omitempty"`
	Installed  string    `json:"installed,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Recorded   bool      `json:"recorded,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var shaPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

type Store struct{ Root string }

func DefaultStore() Store { return Store{Root: filepath.Join(store.DataDir(), "hooks", "install")} }
func (s Store) path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("invalid install job ID")
	}
	return filepath.Join(s.Root, id+".json"), nil
}
func (s Store) Read(id string) (Job, error) {
	var j Job
	p, err := s.path(id)
	if err != nil {
		return j, err
	}
	// A concurrent archive can move p between Stat and ReadJSON. In that case
	// its missing-file zero value falls through to the archived document.
	if _, err = os.Stat(p); err == nil {
		if err = store.ReadJSON(p, &j); err != nil {
			return j, err
		}
		if j.ID != "" {
			return j, nil
		}
	} else if !os.IsNotExist(err) {
		return j, err
	}
	archived := filepath.Join(s.Root, "done", filepath.Base(p))
	if _, err = os.Stat(archived); err != nil {
		return j, err
	}
	err = store.ReadJSON(archived, &j)
	return j, err
}
func (s Store) Update(id string, fn func(*Job) error) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	unlock, err := store.Lock(p + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	target := p
	if _, err = os.Stat(p); os.IsNotExist(err) {
		archived := filepath.Join(s.Root, "done", filepath.Base(p))
		if _, err = os.Stat(archived); err == nil {
			target = archived
		}
	}
	var j Job
	if err = store.ReadJSON(target, &j); err != nil {
		return err
	}
	if err = fn(&j); err != nil {
		return err
	}
	return store.WriteJSON(target, j, 0600)
}

// Archive keeps finished receipts addressable by run ID, without re-reading
// every completed installation during each maintenance pass.
func (s Store) Archive(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	unlock, err := store.Lock(p + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = os.Stat(p); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var j Job
	if err = store.ReadJSON(p, &j); err != nil {
		return err
	}
	if !j.Recorded {
		return fmt.Errorf("cannot archive an unrecorded install")
	}
	dir := filepath.Join(s.Root, "done")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.Rename(p, filepath.Join(dir, filepath.Base(p)))
}
func (s Store) List() ([]Job, error) {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Job{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-5]
		j, err := s.Read(id)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

type Service struct {
	Jobs Store
	Exec processguard.Executor
	Now  func() time.Time
}

func New() Service {
	return Service{Jobs: DefaultStore(), Exec: executeInstall, Now: time.Now}
}
func executeInstall(ctx context.Context, p processguard.Process) error {
	if testing.Testing() {
		return fmt.Errorf("refusing the hook installer in Go tests; use a fake executor")
	}
	return processguard.Execute(ctx, p)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) read(ctx context.Context, id string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	return s.Jobs.Read(id)
}
