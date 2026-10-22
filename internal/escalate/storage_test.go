package escalate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
)

func writerFor(t *testing.T, f *fixture) *state.Writer {
	t.Helper()
	db, err := database.Open(filepath.Dir(f.m.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	return state.For(db)
}

type observedContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestBlockedKeyCancelsWhileOtherKeysAndWritesProceed(t *testing.T) {
	f := newFixture(t)
	w := writerFor(t, f)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := Route(context.Background(), f.m, func(subagent.StartOptions) (subagent.Info, error) {
			close(entered)
			<-release
			return subagent.Info{Meta: subagent.Meta{ID: id(8)}}, nil
		}, Request{Room: room, Key: "held", Brief: "Fix this."})
		done <- err
	}()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedContext{Context: ctx, observed: make(chan struct{})}
	blocked := make(chan error, 1)
	go func() {
		_, _, err := Route(observed, f.m, f.start, Request{Room: room, Key: "held", Brief: "Fix this."})
		blocked <- err
	}()
	<-observed.observed
	cancel()
	if err := <-blocked; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked contender = %v", err)
	}
	workCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := w.Write(workCtx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(workCtx, "UPDATE escalation_records SET profile=profile")
		return err
	}); err != nil {
		t.Fatalf("spawn held writer: %v", err)
	}
	_, spawned, err := Route(workCtx, f.m, func(subagent.StartOptions) (subagent.Info, error) {
		return subagent.Info{Meta: subagent.Meta{ID: id(6)}}, nil
	}, Request{Room: room, Key: "other", Brief: "Fix this."})
	if err != nil || !spawned {
		t.Fatalf("other key blocked: spawned=%v, err=%v", spawned, err)
	}
}

func TestCancelledSpawnStillSavesDedupe(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, spawned, err := Route(ctx, f.m, func(subagent.StartOptions) (subagent.Info, error) {
		cancel()
		return subagent.Info{Meta: subagent.Meta{ID: id(8)}}, nil
	}, Request{Room: room, Key: "cancelled", Brief: "Fix this."})
	if err != nil || !spawned {
		t.Fatalf("started session: spawned=%v, err=%v", spawned, err)
	}
	if _, found, err := readRecord(context.Background(), writerFor(t, f), "cancelled"); err != nil || !found {
		t.Fatalf("dedupe lost: %v, %v", found, err)
	}
	_, spawned, err = Route(context.Background(), f.m, func(subagent.StartOptions) (subagent.Info, error) {
		t.Fatal("cancelled spawn lost its key")
		return subagent.Info{}, nil
	}, Request{Room: room, Key: "cancelled", Brief: "Fix this."})
	if err != nil || spawned {
		t.Fatalf("retry: spawned=%v, err=%v", spawned, err)
	}
}

func TestNewDedupeDoesNotWriteJSON(t *testing.T) {
	f := newFixture(t)
	f.route(t, Request{Key: "copy"})
	path := filepath.Join(dataDir(f.m), "escalations", dedupeHash("copy")[:16]+".json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("new JSON record exists: %v", err)
	}
	rec, found, err := readRecord(context.Background(), writerFor(t, f), "copy")
	if err != nil || !found || rec.Session != id(9) {
		t.Fatalf("dedupe = %+v, %v, %v", rec, found, err)
	}
}

func TestRetainedJSONCannotAffectCurrentRouting(t *testing.T) {
	f := newFixture(t)
	req := Request{Room: room, Key: "ignored-copy", Brief: "Fix this."}
	path := filepath.Join(dataDir(f.m), "escalations", dedupeHash(req.Key)[:16]+".json")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, spawned, err := Route(context.Background(), f.m, func(subagent.StartOptions) (subagent.Info, error) {
		return subagent.Info{Meta: subagent.Meta{ID: id(8)}}, nil
	}, req)
	if err != nil || !spawned {
		t.Fatalf("spawn: spawned=%v, err=%v", spawned, err)
	}
	_, spawned, err = Route(context.Background(), f.m, func(subagent.StartOptions) (subagent.Info, error) {
		t.Fatal("retained JSON released the key")
		return subagent.Info{}, nil
	}, req)
	if err != nil || spawned {
		t.Fatalf("retry: spawned=%v, err=%v", spawned, err)
	}
}
