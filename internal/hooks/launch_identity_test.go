package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/enginesocket"
	"dyna-agent/internal/shellquote"
)

// Hook children use this socket for their first identity check. The listener
// resolves the real peer with SO_PEERCRED. Tests mark the launching process as
// the engine only immediately before execution; the redirected-child test does not.
func hookLaunchTestEngine(t *testing.T, data string) <-chan callerid.Identity {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "hook-engine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("TMPDIR", root)
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	path, err := enginesocket.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := callerid.OpenStore(data)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	resolver, err := callerid.New("/proc", os.Getpid(), records)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	results := make(chan callerid.Identity, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request [1]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil {
					return // enginePID only reads the kernel's peer credentials.
				}
				id := resolver.ResolveConn(conn)
				results <- id
				_ = json.NewEncoder(conn).Encode(id)
			}()
		}
	}()
	t.Setenv("HOOK_IDENTITY_SOCKET", path)
	return results
}

func TestHookLaunchIdentityChild(t *testing.T) {
	if os.Getenv("HOOK_IDENTITY_CHILD") != "1" {
		return
	}
	conn, err := net.Dial("unix", os.Getenv("HOOK_IDENTITY_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var got, want callerid.Identity
	if err := json.NewDecoder(conn).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(os.Getenv("HOOK_IDENTITY_WANT")), &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("first socket caller = %+v; want %+v", got, want)
	}
	self, err := callerid.ProcessOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, forged := range []callerid.Identity{
		{Kind: callerid.Owner},
		{Kind: callerid.Session, Session: "ses_someone_else"},
	} {
		if err := callerid.RecordHook(context.Background(), self, os.Getenv("HOOK_IDENTITY_DATA"), "forged", forged); err == nil {
			t.Fatalf("hook child borrowed another identity: %+v", forged)
		}
	}
}

func TestHookFirstSocketCallKeepsItsPrincipal(t *testing.T) {
	// All cases use one repository and socket. Git identity and sampling have
	// separate coverage; these cases exercise the real launch gate and caller.
	s, repo := setup(t)
	repoID := git(t, repo, "rev-parse", "HEAD")
	result := hookLaunchTestEngine(t, s.DataDir)
	t.Setenv("HOOK_IDENTITY_CHILD", "1")
	t.Setenv("HOOK_IDENTITY_DATA", s.DataDir)
	ordinary := hook("identity", BeforeLand)
	ordinary.OnFail = "block"
	ordinary.Command = fmt.Sprintf("%s -test.run=^TestHookLaunchIdentityChild$", shellquote.Quote(os.Args[0]))
	add(t, s, repo, ordinary)
	scheduled := ordinary
	scheduled.Name, scheduled.Event, scheduled.OnFail, scheduled.EveryMinutes = "scheduled-identity", Schedule, "alert", 1
	add(t, s, repo, scheduled)
	t.Cleanup(authority.InEngine())
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := callerid.OpenStore(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	seenRuns := make(map[string]int)
	for _, tc := range []struct {
		name      string
		lifecycle bool
		scheduled bool
		actor     authority.Actor
		want      callerid.Identity
	}{
		{name: "scheduled", scheduled: true, want: callerid.Identity{Kind: callerid.Owner}},
		{name: "session-lifecycle", lifecycle: true, actor: authority.Actor{Role: authority.Session, Session: "ses_trigger"}, want: callerid.Identity{Kind: callerid.Owner}},
		{name: "explicit-session", actor: authority.Actor{Role: authority.Session, Session: "ses_caller"}, want: callerid.Identity{Kind: callerid.Session, Session: "ses_caller"}},
		{name: "explicit-worker", actor: authority.Actor{Role: authority.Worker, Run: "run_caller", Agent: 3}, want: callerid.Identity{Kind: callerid.Worker, Run: "run_caller", Agent: 3}},
		{name: "explicit-root", actor: authority.Actor{Role: authority.Root, Session: "ses_root"}, want: callerid.Identity{Kind: callerid.Session, Session: "ses_root"}},
		{name: "explicit-owner", actor: authority.Actor{Role: authority.Owner}, want: callerid.Identity{Kind: callerid.Owner}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOOK_IDENTITY_WANT", string(raw))
			h := ordinary
			if tc.scheduled {
				h = scheduled
			}
			ctx := authority.WithActor(t.Context(), tc.actor)
			e := Event{Repo: repo, Worktree: repo, Branch: "main", SHA: repoID, Event: h.Event, Session: "ses_forged_metadata"}
			if tc.scheduled {
				err = s.Dispatch(ctx, e)
			} else if tc.lifecycle {
				err = s.Emit(ctx, e)
			} else {
				_, err = s.Run(ctx, e, h.Name)
			}
			if err != nil {
				runs, _ := s.runs(t.Context(), repoID, h.Name)
				var log []byte
				if len(runs) != 0 {
					log, _ = os.ReadFile(runs[0].LogPath)
				}
				t.Fatalf("hook identity: %v; log: %s", err, log)
			}
			if got := <-result; got != tc.want {
				t.Fatalf("socket caller = %+v; want %+v", got, tc.want)
			}
			seenRuns[h.Name]++
			runs, err := s.runs(t.Context(), repoID, h.Name)
			if err != nil || len(runs) != seenRuns[h.Name] || runs[0].RunnerSession != e.Session {
				t.Fatalf("triggering-session audit = %+v, %v", runs, err)
			}
			var pid int
			var start uint64
			var launcher string
			if err := db.QueryRow("SELECT pid,start,launcher FROM caller_records ORDER BY rowid DESC LIMIT 1").Scan(&pid, &start, &launcher); err != nil || launcher != "hook "+h.Name {
				t.Fatalf("hook principal audit: %q, %v", launcher, err)
			}
			if _, found, err := records.ProcessIdentity(callerid.Process{PID: pid, Start: start + 1}); err != nil || found {
				t.Fatalf("reused anchor PID inherited identity: found=%v, error=%v", found, err)
			}
			if live, err := callerid.ProcessOf(pid); err == nil && live.Start == start {
				t.Fatal("hook anchor remained alive after command exit")
			}
		})
	}
}

func TestHookRecordFailureRunsNothing(t *testing.T) {
	s, repo := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_hook_record BEFORE INSERT ON caller_records BEGIN SELECT RAISE(ABORT, 'test launch record refused'); END"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "must-not-run")
	h := hook("failed-record", AfterLand)
	h.Command = "printf ran > " + shellquote.Quote(marker)
	add(t, s, repo, h)
	t.Cleanup(authority.InEngine())
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	run, err := s.Run(ctx, Event{Repo: repo}, h.Name)
	if err == nil || !strings.Contains(err.Error(), "test launch record refused") || run.Result != "error" {
		t.Fatalf("failed record = %+v, %v", run, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran without its record: %v", err)
	}
}

func TestExplicitHookIgnoresForgedIdentityMetadata(t *testing.T) {
	s, repo := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	marker := filepath.Join(t.TempDir(), "must-not-run")
	h := hook("no-caller", AfterLand)
	h.Command = "printf ran > " + shellquote.Quote(marker)
	add(t, s, repo, h)
	t.Cleanup(authority.InEngine())
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_SESSION", "ses_someone_else")
	run, err := s.Run(t.Context(), Event{Repo: repo, Session: "ses_someone_else"}, h.Name)
	if err == nil || !strings.Contains(err.Error(), "authenticated caller") || run.Result != "error" {
		t.Fatalf("unauthenticated run = %+v, %v", run, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("forged caller ran a command: %v", err)
	}
}

func TestHookRecordRejectsReusedAnchor(t *testing.T) {
	s, _ := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	t.Cleanup(authority.InEngine())
	self, err := callerid.ProcessOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self.Start++
	err = callerid.RecordHook(t.Context(), self, s.DataDir, "reused", callerid.Identity{Kind: callerid.Owner})
	if err == nil || !strings.Contains(err.Error(), "process changed") {
		t.Fatalf("reused anchor accepted: %v", err)
	}
}

func TestHookRedirectedEngineChild(t *testing.T) {
	if os.Getenv("HOOK_REDIRECTED_ENGINE_CHILD") != "1" {
		return
	}
	s, repo := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	marker := filepath.Join(t.TempDir(), "must-not-run")
	h := hook("redirected", AfterLand)
	h.Command = "printf ran > " + shellquote.Quote(marker)
	add(t, s, repo, h)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	run, err := s.Run(ctx, Event{Repo: repo}, h.Name)
	if err == nil || run.Result != "error" {
		t.Fatalf("unrecorded child gained Owner through its own socket: %+v, %v", run, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unrecorded child ran a command: %v", err)
	}
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var records int
	if err := db.QueryRow("SELECT count(*) FROM caller_records").Scan(&records); err != nil || records != 0 {
		t.Fatalf("unrecorded child wrote %d identities: %v", records, err)
	}
}

func TestUnrecordedHookChildRejectsRedirectedEngineSocket(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHookRedirectedEngineChild$")
	cmd.Env = append(os.Environ(), "HOOK_REDIRECTED_ENGINE_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("redirected socket child: %v\n%s", err, output)
	}
}
