package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// This starts the real binary, including the model proxy, runtime and owner
// socket. No engine or CI service is replaced by a fixture. Run with:
//
//	go build -o /tmp/dyna-credential-test ./cmd/dyna
//	DYNA_TEST_ENGINE=/tmp/dyna-credential-test go test ./cmd/dyna -run '^TestEngineWithoutGitHubCredential$' -count=1 -v
//
// It uses a temporary store with CI enabled for a repository and no hosts, and
// waits through two real 30-second CI ticks. It does not test host placement or
// authenticated GitHub operations.
func TestEngineWithoutGitHubCredential(t *testing.T) {
	binary := os.Getenv("DYNA_TEST_ENGINE")
	if binary == "" {
		t.Skip("set DYNA_TEST_ENGINE to a freshly built dyna binary for a real engine start")
	}
	// Keep Unix socket paths short, independently of the test's name.
	dir, err := os.MkdirTemp("", "ci-credential-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data, runtime := filepath.Join(dir, "data"), filepath.Join(dir, "run")
	profiles := filepath.Join(dir, "profiles.json")
	if err := os.WriteFile(profiles, []byte(`{"test":{"provider":"openai","model":"test","allowedModels":["test"],"thinking":"off"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, err := range []error{
		st.CreateRoom(ctx, "room-test", "test", time.Now()),
		st.AddRoomRepo(ctx, "room-test", "owner/repo", true),
		st.SetRepoCI(ctx, "owner/repo", true, 0),
		st.Close(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	logPath := filepath.Join(dir, "engine.log")
	logs, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	cmd := exec.Command(binary, "engine", "--data", data, "--runtime", runtime, "--host-data", data, "--profiles", profiles)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(value string) bool { return strings.HasPrefix(value, "GH_TOKEN=") })
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("engine did not shut down within 10 seconds")
		}
		if waitErr != nil {
			t.Errorf("engine exit: %v", waitErr)
		}
	}()

	client := newClient(filepath.Join(runtime, "owner.sock"))
	deadline := time.Now().Add(90 * time.Second)
	var offAt time.Time
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
			t.Fatalf("engine exited before the regression finished: %v\n%s", waitErr, body)
		default:
		}
		count := strings.Count(string(body), "CI pool off: no GitHub credential")
		if count > 1 {
			t.Fatalf("pool logged off more than once:\n%s", body)
		}
		if count == 1 {
			if offAt.IsZero() {
				offAt = time.Now()
			}
			requestCtx, cancel := context.WithTimeout(ctx, time.Second)
			repos, err := client.RoomRepos(requestCtx, contract.RoomReposRequest{Room: "room-test"})
			cancel()
			if err != nil || len(repos.Repos) != 1 || repos.Repos[0].Repo != "owner/repo" {
				t.Fatalf("engine not serving the owner socket with CI off: %+v, %v\n%s", repos, err, body)
			}
			if time.Since(offAt) >= 31*time.Second {
				t.Log("real engine stayed responsive for a further CI tick after the pool turned off; off line appeared once")
				return
			}
		}
		time.Sleep(200 * time.Millisecond) // timing: sample responsiveness through the next CI tick.
	}
	body, _ := os.ReadFile(logPath)
	t.Fatalf("engine did not report CI pool off and remain up through another tick:\n%s", body)
}
