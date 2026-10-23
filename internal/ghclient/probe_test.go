package ghclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The fake gh is a shell script, so each probe starts in milliseconds. It
// replays canned files from GH_PROBE_DIR. A FIFO named "started" tells the test
// that the API call began. A FIFO named "release" holds the call until the test
// opens it.
const fakeGH = `#!/bin/sh
d=$GH_PROBE_DIR
case "$1 $2" in
"auth status") printf '%s' '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") printf test-secret;;
*)
	echo >> "$d/calls"
	[ -p "$d/started" ] && echo > "$d/started"
	[ -p "$d/release" ] && { read _ < "$d/release"; }
	cat "$d/stdout"
	cat "$d/stderr" >&2
	exit "$(cat "$d/exit")";;
esac
`

type probeReply struct {
	code    int
	headers string
	body    string
	hold    bool
}

// write mirrors `gh api --include`. A zero code mirrors a network failure.
func (p probeReply) write(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{"stdout": "", "stderr": "network failed test-secret", "exit": "1"}
	if p.code != 0 {
		files["stdout"] = fmt.Sprintf("HTTP/1.1 %d X\r\n%sContent-Length: %d\r\n\r\n%s", p.code, p.headers, len(p.body), p.body)
		files["stderr"], files["exit"] = "", "0"
		if p.code >= 400 {
			files["stderr"], files["exit"] = fmt.Sprintf("gh: test-secret (HTTP %d)", p.code), "1"
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// holdFIFOs creates the two FIFOs and opens both read-write and nonblocking.
// The test then never blocks on an open, and reads honor a deadline.
func holdFIFOs(t *testing.T, dir string) (started, release *os.File) {
	t.Helper()
	var files []*os.File
	for _, name := range []string{"started", "release"} {
		path := filepath.Join(dir, name)
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	// Free any fake gh that is still waiting, then close.
	t.Cleanup(func() {
		files[1].Write([]byte("\n"))
		files[0].Close()
		files[1].Close()
	})
	return files[0], files[1]
}

// probeWait bounds every wait in these tests. Passing runs never reach it.
const probeWait = 900 * time.Millisecond

type probeEnv struct {
	exe, dir, key    string
	r                repository
	started, release *os.File
}

// probeRoom builds the room and fake gh once. Table tests call reply per case,
// so they pay for the git repository once.
func probeRoom(t *testing.T) probeEnv {
	t.Helper()
	records := setupRooms(t, "resident-labs/project")
	exe := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(exe, []byte(fakeGH), 0700); err != nil {
		t.Fatal(err)
	}
	r := repository{"github.com", "resident-labs/project"}
	cmd := Command(context.Background(), exe, records[0].Repo)
	return probeEnv{exe: exe, key: accountKey(cmd, r) + records[0].ID + ReadWrite, r: r}
}

// reply returns an environment where the fake gh answers with the given reply.
func (e probeEnv) reply(t *testing.T, reply probeReply) probeEnv {
	t.Helper()
	e.dir = t.TempDir()
	reply.write(t, e.dir)
	if reply.hold {
		e.started, e.release = holdFIFOs(t, e.dir)
	}
	t.Setenv("GH_PROBE_DIR", e.dir)
	forgetAccount(e.key)
	return e
}

func (e probeEnv) calls() int {
	raw, _ := os.ReadFile(filepath.Join(e.dir, "calls"))
	return strings.Count(string(raw), "\n")
}

// waitStarted waits until the fake gh reaches its API call.
func (e probeEnv) waitStarted(t *testing.T) {
	t.Helper()
	e.started.SetReadDeadline(time.Now().Add(probeWait))
	if _, err := e.started.Read(make([]byte, 1)); err != nil {
		t.Fatalf("API call did not start: %v", err)
	}
}

// releaseCall lets a held API call finish.
func (e probeEnv) releaseCall(t *testing.T) {
	t.Helper()
	if _, err := e.release.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
}

// recv returns the next value from ch or fails the test after probeWait.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(probeWait):
		t.Fatal("timed out waiting for the probe")
		panic("unreachable")
	}
}

func (e probeEnv) seed(login string) {
	config := ghConfigFingerprint(Command(context.Background(), e.exe, ""))
	accounts.Lock()
	accounts.choices[e.key] = choice{login: login, until: time.Now().Add(-time.Second), config: config}
	accounts.Unlock()
}

func (e probeEnv) choice() choice {
	accounts.Lock()
	defer accounts.Unlock()
	return accounts.choices[e.key]
}

func (e probeEnv) selectAccount(ctx context.Context) (string, error) {
	login, _, err := selectAccount(ctx, Command(ctx, e.exe, ""), e.r)
	return login, err
}

var transientCases = []struct {
	name  string
	reply probeReply
	cause string
}{
	{"secondary rate limit", probeReply{code: 403, body: `{"message":"You have exceeded a secondary rate limit"}`}, "rate limited (HTTP 403)"},
	{"rate limit header", probeReply{code: 403, headers: "Retry-After: 30\r\n", body: `{"message":"Forbidden"}`}, "rate limited (HTTP 403)"},
	{"primary rate limit", probeReply{code: 403, headers: "X-RateLimit-Remaining: 0\r\n", body: `{"message":"Forbidden"}`}, "rate limited (HTTP 403)"},
	{"too many requests", probeReply{code: 429, body: `{}`}, "rate limited (HTTP 429)"},
	{"server unavailable", probeReply{code: 503, body: `{}`}, "HTTP 503"},
	{"network", probeReply{}, "network"},
}

// With no known account, a transient failure is reported without secrets.
func TestAccountProbeTransientFailures(t *testing.T) {
	room := probeRoom(t)
	for _, tc := range transientCases {
		t.Run(tc.name, func(t *testing.T) {
			env := room.reply(t, tc.reply)
			_, err := env.selectAccount(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.cause) || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("cause missing or unsafe: %v", err)
			}
			if n := env.calls(); n != 1 {
				t.Fatalf("probes=%d", n)
			}
		})
	}
}

// With a known account, a transient failure keeps it and extends the cache.
func TestAccountProbeTransientFailuresKeepKnownAccount(t *testing.T) {
	room := probeRoom(t)
	for _, tc := range transientCases {
		t.Run(tc.name, func(t *testing.T) {
			env := room.reply(t, tc.reply)
			env.seed("writer")
			if login, err := env.selectAccount(context.Background()); err != nil || login != "writer" {
				t.Fatalf("lost known account: login=%q err=%v", login, err)
			}
			if until := time.Until(env.choice().until); until < 29*time.Minute {
				t.Fatalf("cache was not extended: %v", until)
			}
			if _, err := env.selectAccount(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := env.calls(); n != 1 {
				t.Fatalf("probes=%d", n)
			}
		})
	}
}

func TestAccountProbeDefinitiveDenial(t *testing.T) {
	room := probeRoom(t)
	for _, code := range []int{200, 401, 403, 404} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			env := room.reply(t, probeReply{code: code, body: `{"permissions":{"push":false}}`})
			env.seed("writer")
			_, err := env.selectAccount(context.Background())
			if err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account can write") {
				t.Fatalf("denial=%v", err)
			}
			if env.choice().login != "" {
				t.Fatal("denied account retained for outage fallback")
			}
		})
	}
}

func TestAccountProbeConcurrentCallers(t *testing.T) {
	env := probeRoom(t).reply(t, probeReply{code: 200, body: `{"permissions":{"push":true}}`, hold: true})
	ctx := context.Background()
	results := make(chan error, 12)
	var ready sync.WaitGroup
	ready.Add(cap(results))
	for i := 0; i < cap(results); i++ {
		go func() {
			ready.Done()
			_, err := env.selectAccount(ctx)
			results <- err
		}()
	}
	// Hold the one probe open until every caller has started, so all of them join it.
	ready.Wait()
	env.waitStarted(t)
	env.releaseCall(t)
	for i := 0; i < cap(results); i++ {
		if err := recv(t, results); err != nil {
			t.Fatal(err)
		}
	}
	if n := env.calls(); n != 1 {
		t.Fatalf("concurrent callers made %d probes", n)
	}
	if until := time.Until(env.choice().until); until < 29*time.Minute {
		t.Fatalf("cache duration=%v", until)
	}
}

func TestAccountProbeCallerCancellation(t *testing.T) {
	env := probeRoom(t).reply(t, probeReply{code: 200, body: `{"permissions":{"push":true}}`, hold: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := env.selectAccount(ctx); done <- err }()
	env.waitStarted(t)
	cancel()
	if err := recv(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	// The probe outlives its first caller. A second caller joins it.
	next := make(chan error, 1)
	go func() {
		login, err := env.selectAccount(context.Background())
		if err == nil && login != "writer" {
			err = fmt.Errorf("login=%q", login)
		}
		next <- err
	}()
	env.releaseCall(t)
	if err := recv(t, next); err != nil {
		t.Fatalf("shared probe was cancelled: %v", err)
	}
	if n := env.calls(); n != 1 {
		t.Fatalf("probe restarted %d times", n)
	}
}

func TestAccountProbeSharedDeadline(t *testing.T) {
	room := probeRoom(t)
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", cached), func(t *testing.T) {
			ctx, expire := context.WithCancelCause(context.Background())
			defer expire(context.Canceled)
			env := room.reply(t, probeReply{code: 200, hold: true})
			previous := choice{}
			if cached {
				env.seed("writer")
				previous = env.choice()
			}
			type result struct {
				login string
				err   error
			}
			done := make(chan result, 1)
			go func() {
				login, err := refreshAccount(ctx, Command(ctx, env.exe, ""), env.r, env.key, previous, true)
				done <- result{login, err}
			}()
			// Expire the shared deadline only after the API call starts. This proves
			// that the token was available before the stalled probe.
			env.waitStarted(t)
			expire(context.DeadlineExceeded)
			res := recv(t, done)
			if cached {
				if res.err != nil || res.login != "writer" {
					t.Fatalf("deadline lost known account: login=%q err=%v", res.login, res.err)
				}
				if until := time.Until(env.choice().until); until < 29*time.Minute {
					t.Fatalf("deadline did not extend the cache: %v", until)
				}
			} else if res.err == nil || !strings.Contains(res.err.Error(), "timed out") || strings.Contains(res.err.Error(), "test-secret") {
				t.Fatalf("deadline cause missing or unsafe: %v", res.err)
			}
		})
	}
}
