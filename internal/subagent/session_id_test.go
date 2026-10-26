package subagent

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// A caller-supplied session ID must be free when the session is written, even
// when two starts passed the early check together: both would otherwise use
// one session directory.
func TestConcurrentStartsWithOneIDCreateOneSession(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	dir := t.TempDir()
	start := func(name string) error {
		_, _, err := e.m.Start(context.Background(), StartOptions{
			ID: id, Name: name, Dir: dir, Profile: "mock", Goal: "One session", Task: "RESPOND: ready",
		})
		return err
	}
	errs := make([]error, 2)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for i, name := range []string{"first", "second"} {
		go func() {
			defer wg.Done()
			<-begin
			errs[i] = start(name)
		}()
	}
	close(begin)
	wg.Wait()
	var held, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			held++
		case strings.Contains(err.Error(), "already exists"):
			refused++
		default:
			t.Fatalf("start = %v", err)
		}
	}
	if held != 1 || refused != 1 {
		t.Fatalf("starts = %v, want one session and one refusal", errs)
	}
	if _, err := e.m.Store.ReadMeta(id); err != nil {
		t.Fatalf("the session was not written: %v", err)
	}
}

// A short caller-supplied ID is refused before a generated name reads its
// suffix, and never reaches the session directory.
func TestStartRefusesAShortSessionID(t *testing.T) {
	e := newTestEnv(t)
	_, _, err := e.m.Start(context.Background(), StartOptions{
		ID: "ses_x", Dir: t.TempDir(), Profile: "mock", Goal: "Short id", Task: "RESPOND: ready",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid session id") {
		t.Fatalf("short id start = %v, want an invalid-ID refusal", err)
	}
	if _, err := e.m.Store.ReadMeta("ses_x"); err == nil {
		t.Fatal("a short ID wrote a session")
	}
}

// ValidID accepts what NewID mints and refuses a truncated shape.
func TestValidIDFollowsNewID(t *testing.T) {
	for _, id := range []string{NewID(), "ses_20261017-000001-aaaaaaaa"} {
		if !ValidID(id) {
			t.Fatalf("ValidID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "ses_x", "ses_20261017-000001", "ses_20261017-000001-aaaaaaa", "ses_20261317-000001-aaaaaaaa", "ses_20261017-250001-aaaaaaaa"} {
		if ValidID(id) {
			t.Fatalf("ValidID(%q) = true, want false", id)
		}
	}
}

// A start repeated after an engine restart names a session that already
// exists. It gets the duplicate-ID refusal, not a conflict with its own name.
func TestRepeatedNamedStartReportsTheExistingSession(t *testing.T) {
	e := newTestEnv(t)
	opts := StartOptions{ID: NewID(), Name: "kid", Dir: t.TempDir(), Profile: "mock", Goal: "One session", Task: "RESPOND: ready"}
	if _, _, err := e.m.Start(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.m.Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "session "+opts.ID+" already exists") {
		t.Fatalf("repeated start = %v; want the already-exists refusal", err)
	}
}
