// Package hookinstall implements the opt-in Dyna install hook. A durable job
// connects a detached systemd unit to the hook run that started it.
package hookinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/processguard"
	statewrite "dyna-agent/internal/state"
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

// Store.Root is <data dir>/hooks/install. Locks and output logs stay there.
type Store struct{ Root string }

func DefaultStore() Store { return Store{Root: filepath.Join(store.DataDir(), "hooks", "install")} }
func (s Store) path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("invalid install job ID")
	}
	return filepath.Join(s.Root, id+".json"), nil
}
func (s Store) Read(id string) (Job, error) {
	if _, err := s.path(id); err != nil {
		return Job{}, err
	}
	db, err := s.db()
	if err != nil {
		return Job{}, err
	}
	var body []byte
	err = db.QueryRow("SELECT body FROM hook_install_jobs WHERE id=?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, os.ErrNotExist
	}
	if err != nil {
		return Job{}, err
	}
	return decodeJob(body)
}

func decodeJob(body []byte) (Job, error) {
	var j Job
	err := json.Unmarshal(body, &j)
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
	j, err := s.Read(id)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := fn(&j); err != nil {
		return err
	}
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO hook_install_jobs(id,body) VALUES(?,?)
ON CONFLICT(id) DO UPDATE SET body=excluded.body`, id, body)
		return err
	})
}

// Archive keeps finished receipts addressable by run ID, without re-reading
// every completed installation during each maintenance pass.
func (s Store) Archive(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	unlock, err := store.Lock(p + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	j, err := s.Read(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !j.Recorded {
		return fmt.Errorf("cannot archive an unrecorded install")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "UPDATE hook_install_jobs SET archived=1 WHERE id=?", id)
		return err
	})
}

// List returns active jobs in ID order.
func (s Store) List() ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT body FROM hook_install_jobs WHERE archived=0 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		j, err := decodeJob(body)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
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
