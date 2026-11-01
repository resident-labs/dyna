package git

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// gatedRunner holds each fetch open until the test releases it, and records the order of the work.
type gatedRunner struct {
	fakeRunner
	mu      sync.Mutex
	started chan contract.RepoName
	release chan struct{}
	fetches atomic.Int32
}

func newGatedRunner() *gatedRunner {
	return &gatedRunner{started: make(chan contract.RepoName, 64), release: make(chan struct{}, 64)}
}

func (r *gatedRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	if len(args) > 2 && args[2] == "fetch" {
		r.fetches.Add(1)
		r.started <- contract.RepoName(args[1])
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, errors.New("signal: killed") // what exec reports, not ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakeRunner.Run(ctx, env, args...)
}

type countingCache struct {
	pushes atomic.Int32
}

func (c *countingCache) PushCache(context.Context, string, string, []string) error {
	c.pushes.Add(1)
	// A real push takes time, which is what lets callers overlap and share it.
	time.Sleep(20 * time.Millisecond)
	return nil
}

// mirrorFor returns a Mirror whose repositories already have a mirror folder, so Prepare fetches instead of cloning.
func mirrorFor(t *testing.T, r Runner, repos ...contract.RepoName) *Mirror {
	t.Helper()
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: r}
	for _, repo := range repos {
		path, err := m.Layout.Git(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// started waits for a fetch to begin and names its mirror.
func started(t *testing.T, r *gatedRunner) contract.RepoName {
	t.Helper()
	select {
	case repo := <-r.started:
		return repo
	case <-time.After(5 * time.Second):
		t.Fatal("no fetch started")
		return ""
	}
}

func TestSimultaneousStartsShareTwoFetchesAtMost(t *testing.T) {
	r := newGatedRunner()
	c := &countingCache{}
	m := mirrorFor(t, r, "owner/repo")
	var wg sync.WaitGroup
	prepare := func(session contract.SessionID) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Prepare(context.Background(), c, "owner/repo", session, true); err != nil {
				t.Error(err)
			}
		}()
	}
	prepare("ses_first")
	started(t, r)
	for _, session := range []contract.SessionID{"ses_2", "ses_3", "ses_4", "ses_5", "ses_6", "ses_7", "ses_8"} {
		prepare(session)
	}
	// All seven join the one run queued behind the fetch that is open.
	time.Sleep(50 * time.Millisecond)
	r.release <- struct{}{}
	started(t, r)
	r.release <- struct{}{}
	wg.Wait()
	if got := r.fetches.Load(); got != 2 {
		t.Fatalf("eight starts took %d fetches", got)
	}
	if got := c.pushes.Load(); got > 3 {
		t.Fatalf("eight starts took %d pushes", got)
	}
}

// A push to GitHub just before a start must reach its box, so a fetch already running cannot serve that start.
func TestACallerNeverTakesAFetchThatBeganBeforeItAsked(t *testing.T) {
	r := newGatedRunner()
	c := &countingCache{}
	m := mirrorFor(t, r, "owner/repo")
	first := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), c, "owner/repo", "ses_1", true)
		first <- err
	}()
	started(t, r)
	late := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), c, "owner/repo", "ses_2", true)
		late <- err
	}()
	time.Sleep(20 * time.Millisecond)
	r.release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-late:
		t.Fatalf("late caller returned on the earlier fetch: %v", err)
	case repo := <-r.started:
		if repo == "" {
			t.Fatal("no repository")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no second fetch")
	}
	r.release <- struct{}{}
	if err := <-late; err != nil {
		t.Fatal(err)
	}
}

func TestOneRepositoryDoesNotBlockAnother(t *testing.T) {
	r := newGatedRunner()
	c := &countingCache{}
	m := mirrorFor(t, r, "owner/slow", "owner/quick")
	slow := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), c, "owner/slow", "ses_1", true)
		slow <- err
	}()
	started(t, r)
	quick := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), c, "owner/quick", "ses_2", true)
		quick <- err
	}()
	started(t, r)
	r.release <- struct{}{}
	r.release <- struct{}{}
	for _, done := range []chan error{slow, quick} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadsDoNotWaitForAFetch(t *testing.T) {
	r := newGatedRunner()
	c := &countingCache{}
	m := mirrorFor(t, r, "owner/repo")
	done := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), c, "owner/repo", "ses_1", true)
		done <- err
	}()
	started(t, r)
	// A box's hello reads the mirror this way.
	read := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), c, "owner/repo", "ses_2", false)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read waited for a fetch")
	}
	r.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEachHostGetsItsOwnPushFromOneFetch(t *testing.T) {
	r := newGatedRunner()
	hosts := []*countingCache{{}, {}}
	m := mirrorFor(t, r, "owner/repo")
	first := make(chan error, 1)
	go func() {
		_, err := m.Prepare(context.Background(), hosts[0], "owner/repo", "ses_1", true)
		first <- err
	}()
	started(t, r)
	r.release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	r.release <- struct{}{}
	if _, err := m.Prepare(context.Background(), hosts[1], "owner/repo", "ses_2", true); err != nil {
		t.Fatal(err)
	}
	if hosts[0].pushes.Load() != 1 || hosts[1].pushes.Load() != 1 {
		t.Fatalf("pushes %d and %d", hosts[0].pushes.Load(), hosts[1].pushes.Load())
	}
}

// Closing the box that runs a fetch kills git, which must not fail the boxes that were waiting on it.
func TestAWaiterSurvivesTheWorkerBeingCancelled(t *testing.T) {
	var s shared
	started := make(chan int, 4)
	block, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- s.do(context.Background(), "key", func(context.Context) error {
			started <- -1
			<-block
			return nil
		})
	}()
	<-started
	var cancels [2]context.CancelFunc
	outcome := [2]chan error{make(chan error, 1), make(chan error, 1)}
	for i := range cancels {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		defer cancel()
		go func() {
			outcome[i] <- s.do(ctx, "key", func(ctx context.Context) error {
				started <- i
				select {
				case <-ctx.Done():
					return errors.New("signal: killed") // what exec reports, not ctx.Err()
				case <-release:
					return nil
				}
			})
		}()
	}
	// Both join the run queued behind the first, and either may end up doing it.
	time.Sleep(50 * time.Millisecond)
	close(block)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	worker := <-started
	cancels[worker]()
	if err := <-outcome[worker]; err == nil {
		t.Fatal("the cancelled worker reported success")
	}
	select {
	case next := <-started:
		if next == worker {
			t.Fatal("the cancelled caller worked again")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter gave up with the cancelled worker")
	}
	close(release)
	if err := <-outcome[1-worker]; err != nil {
		t.Fatalf("waiter failed with another caller's cancellation: %v", err)
	}
}

func TestASharedFetchFailureReachesEveryCallerSharingIt(t *testing.T) {
	r := &failingFetch{}
	m := mirrorFor(t, r, "owner/repo")
	s := make(chan error, 2)
	for _, session := range []contract.SessionID{"ses_1", "ses_2"} {
		go func() {
			_, err := m.Prepare(context.Background(), &countingCache{}, "owner/repo", session, true)
			s <- err
		}()
	}
	for range 2 {
		if err := <-s; err == nil {
			t.Fatal("a failed fetch reported success")
		}
	}
}

type failingFetch struct{ fakeRunner }

func (r *failingFetch) Run(_ context.Context, _ []string, args ...string) ([]byte, error) {
	return nil, errors.New("git failed")
}

func TestAFailedCloneLeavesNoMirrorForReaders(t *testing.T) {
	r := &fakeRunner{fail: "clone"}
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: r}
	if _, err := m.Prepare(context.Background(), &countingCache{}, "owner/repo", "ses_1", true); err == nil {
		t.Fatal("clone failure ignored")
	}
	path, err := m.Layout.Git("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a failed clone left a mirror: %v", err)
	}
}
