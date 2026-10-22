package lanesync

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"dyna-agent/internal/database"
)

// A real Git fetch blocks in its upload-pack process. An operation update
// must still commit, and the fetch must preserve that concurrent update.
func TestFetchDoesNotHoldTheStoreLock(t *testing.T) {
	dir := t.TempDir()
	remote, repo := filepath.Join(dir, "remote.git"), filepath.Join(dir, "repo")
	git(t, dir, "init", "--bare", "--initial-branch=main", remote)
	git(t, dir, "init", "-b", "main", repo)
	git(t, repo, "remote", "add", "origin", remote)
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	for _, path := range []string{ready, release} {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	signal, err := os.OpenFile(ready, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { signal.Close() })
	releaseSignal, err := os.OpenFile(release, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseSignal.Close() })
	script := filepath.Join(dir, "upload-pack")
	body := "#!/bin/sh\nprintf 'ready\\n' > '" + ready + "'\nread line < '" + release + "'\nexec /usr/bin/git-upload-pack \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "remote.origin.uploadpack", script)
	s := Service{Dir: filepath.Join(dir, "state")}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(s.Dir) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			if _, err := releaseSignal.WriteString("go\n"); err != nil {
				t.Error(err)
			}
		})
	}
	fetched := make(chan error, 1)
	fetchDone := make(chan struct{})
	go func() {
		defer close(fetchDone)
		fetched <- s.Fetch(ctx, []string{repo})
	}()
	defer func() {
		unblock()
		cancel()
		<-fetchDone
	}()
	announced := make(chan error, 1)
	go func() { _, err := bufio.NewReader(signal).ReadString('\n'); announced <- err }()
	select {
	case err := <-announced:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-fetched:
		t.Fatalf("fetch ended before upload-pack: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	other := Service{Dir: s.Dir}
	if err := other.Update(func(st *Snapshot) error {
		st.Operations[repo] = Operation{Progress: Progress{State: "building"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-fetched; err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil || st.Operations[repo].State != "building" {
		t.Fatalf("fetch lost concurrent operation: %+v, %v", st, err)
	}
}
