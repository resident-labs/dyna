package runtime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/store"
)

type restartBarrierRepos struct{ beforeReplacementCreate }

func (*restartBarrierRepos) Ref(context.Context, contract.RepoName, string) (string, error) {
	return "", nil
}
func (*restartBarrierRepos) Unavailable(context.Context, contract.RepoName) (map[string]bool, error) {
	return nil, nil
}
func (*restartBarrierRepos) Seed(context.Context, contract.SessionID, contract.HostName) ([]contract.BoxRepo, string, error) {
	return nil, "", nil
}

type unexpectedRestartHost struct{}

func (unexpectedRestartHost) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("restart touched new host before cleaning old resources")
}

type restartFixture struct {
	st                                            *store.Store
	svc                                           *engine.Service
	runtime                                       *Boxes
	root, workspace, stack, log, entered, release string
	now                                           time.Time
}

func newRestartFixture(t *testing.T) *restartFixture {
	t.Helper()
	f := &restartFixture{root: t.TempDir(), now: time.Unix(100000, 0)}
	f.workspace = filepath.Join(f.root, "workspace")
	f.stack = filepath.Join(f.root, "stack")
	f.log = filepath.Join(f.root, "commands")
	f.entered = filepath.Join(f.root, "entered")
	f.release = filepath.Join(f.root, "release")
	for _, dir := range []string{f.workspace, f.stack} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "old-work"), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tools := t.TempDir()
	// This is a real framed shell/Podman Host. Only the podman executable is a
	// filesystem fixture: each named-volume command deletes its own directory.
	script := `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$RESTART_LOG"
if [ "${2:-}" = stop ] && [ "${RESTART_GATE:-}" = yes ]; then
 : > "$RESTART_ENTERED"
 while [ ! -f "$RESTART_RELEASE" ]; do sleep 0.01; done
fi
case " $* " in
 *' rm --ignore --volumes '*) [ "${RESTART_FAIL:-}" != container ] || exit 71 ;;
 *' volume rm '*)
  [ "${RESTART_FAIL:-}" != workspace ] || exit 72
  rm -rf -- "$RESTART_WORKSPACE"
  [ "${RESTART_FAIL:-}" != uncertain ] || exit 74 ;;
 *' volume prune '*)
  [ "${RESTART_FAIL:-}" != stack ] || exit 73
  rm -rf -- "$RESTART_STACK" ;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "systemctl"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	for key, value := range map[string]string{"RESTART_LOG": f.log, "RESTART_WORKSPACE": f.workspace, "RESTART_STACK": f.stack, "RESTART_ENTERED": f.entered, "RESTART_RELEASE": f.release, "XDG_RUNTIME_DIR": t.TempDir()} {
		t.Setenv(key, value)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	t.Cleanup(func() { st.Close() })
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) VALUES('old','','ready',64,137438953472,1099511627776,?,?),('new','','ready',64,137438953472,1099511627776,?,?);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','retained goal',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want,due_at,failures,create_retry_host) VALUES('b','s','old','used-image','agent','failed','original failure','',10,20,'stop',30,3,'old');
INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('used','s',1,2,'completed','p',0,0,0,0);
INSERT INTO room_leftovers(host,room,box,slice) VALUES('old','r','b','');`, f.now.UnixMilli(), f.now.UnixMilli(), f.now.UnixMilli(), f.now.UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sockets := api.NewSockets(nil, t.TempDir(), uint32(os.Getuid()), nil)
	t.Cleanup(func() { sockets.Close() })
	h, err := podman.New("user@host", t.TempDir(), "old", &nativePinnedRunner{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newHost, err := podman.New("user@new-host", t.TempDir(), "new", unexpectedRestartHost{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = &Boxes{Store: st, Sockets: sockets, Layout: box.Layout{Data: f.root, HostData: f.root}, Runtime: t.TempDir(), hosts: map[contract.HostName]*podman.Host{"old": h, "new": newHost}, restored: map[contract.HostName]bool{"old": true}}
	f.svc = engine.New(st, f.runtime, stackProfiles{}, func() time.Time { return f.now })
	t.Cleanup(func() { _ = os.WriteFile(f.release, nil, 0600); f.svc.Reconciler().Wait() })
	return f
}
func (f *restartFixture) exec(t *testing.T, query string) {
	t.Helper()
	if err := f.st.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(query); return err }); err != nil {
		t.Fatal(err)
	}
}
func (f *restartFixture) snapshot(t *testing.T) string {
	t.Helper()
	var raw string
	if err := f.st.Read().QueryRow(`SELECT json_object('id',id,'session',session,'host',host,'state',state,'want',want,'image',image,'reason',reason,'created',created_at,'updated',updated_at,'due',due_at,'failures',failures,'retry',create_retry_host,'slice',slice) FROM boxes WHERE id='b'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
func (f *restartFixture) restart(ctx context.Context) *contract.Error {
	_, problem := f.svc.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionRestartRequest{Session: "s"})
	return problem
}
func (f *restartFixture) restartAsync(t *testing.T, ctx context.Context) <-chan *contract.Error {
	t.Helper()
	result := make(chan *contract.Error, 1)
	finished := make(chan struct{})
	go func() { result <- f.restart(ctx); close(finished) }()
	t.Cleanup(func() { _ = os.WriteFile(f.release, nil, 0600); <-finished })
	return result
}

func assertRestartFile(t *testing.T, dir, name string, present bool) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, name))
	if (present && err != nil) || (!present && !os.IsNotExist(err)) {
		t.Errorf("%s/%s present=%v: %v", dir, name, present, err)
	}
}

func TestSessionRestartDeletesBeforeSameRowReset(t *testing.T) {
	for _, scenario := range []string{"healthy", "quarantined-old-host"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRestartFixture(t)
			ctx := context.Background()
			if scenario == "quarantined-old-host" {
				f.exec(t, `UPDATE hosts SET create_problem='create_failed' WHERE name='old'`)
			}
			if problem := f.restart(ctx); problem != nil {
				t.Fatal(problem)
			}
			b, err := f.st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			wantHost := contract.HostName("old")
			if scenario == "quarantined-old-host" {
				wantHost = ""
			}
			if b.ID != "b" || b.Session != "s" || b.State != contract.BoxPending || b.Image != "" || b.Host != wantHost {
				t.Fatalf("reset: %+v", b)
			}
			var state, goal string
			if err := f.st.Read().QueryRow(`SELECT state,goal FROM sessions WHERE id='s'`).Scan(&state, &goal); err != nil || state != "open" || goal != "retained goal" {
				t.Fatalf("retained session: %s %q %v", state, goal, err)
			}
			debts, err := f.st.Leftovers(ctx, "old")
			if err != nil || len(debts) != 0 {
				t.Fatalf("stale debt: %+v %v", debts, err)
			}
			for _, dir := range []string{f.workspace, f.stack} {
				assertRestartFile(t, dir, "old-work", false)
			}
			raw, err := os.ReadFile(f.log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "rm --ignore --volumes b") || !strings.Contains(string(raw), "volume rm") || !strings.Contains(string(raw), "volume prune") {
				t.Fatalf("incomplete removal: %s", raw)
			}
			// Ordinary lifecycle will now create fresh volumes under the SAME names.
			// Model that later used incarnation, including recovery with an empty image.
			for _, dir := range []string{f.workspace, f.stack} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "new-work"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.exec(t, `UPDATE boxes SET host='old',state='starting',want='run',image='fresh-image',due_at=0,updated_at=21 WHERE id='b'`)
			starting, err := f.st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.TransitionBox(ctx, starting, contract.BoxReady, "", contract.Event{ID: "fresh-ready", Room: "r", Time: f.now.Add(time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}); err != nil {
				t.Fatal(err)
			}
			f.exec(t, `INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('fresh-used','s',3,4,'completed','p',0,0,0,0);
UPDATE boxes SET state='creating',image='',updated_at=22 WHERE id='b'`)
			if problem := f.restart(ctx); problem == nil || problem.Code != contract.ErrConflict {
				t.Fatalf("stale caller: %v", problem)
			}
			repos := &restartBarrierRepos{}
			f.svc.SetRepositories(repos)
			if err := f.svc.Reconciler().Pass(ctx); err != nil {
				t.Fatal(err)
			}
			f.svc.Reconciler().Wait()
			if !repos.entered {
				t.Fatal("used replacement did not reach recovery Remove before Create")
			}
			for _, dir := range []string{f.workspace, f.stack} {
				assertRestartFile(t, dir, "new-work", true)
			}
		})
	}
}

func TestSessionRestartFailurePreservesFailedMetadata(t *testing.T) {
	for _, mode := range []string{"unreachable", "container", "workspace", "stack", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestartFixture(t)
			if mode == "unreachable" {
				f.exec(t, `UPDATE hosts SET state='unreachable' WHERE name='old'`)
			} else {
				t.Setenv("RESTART_FAIL", mode)
			}
			before := f.snapshot(t)
			if problem := f.restart(context.Background()); problem == nil || problem.Code != contract.ErrUnavailable {
				t.Fatalf("cleanup failure: %v", problem)
			}
			if after := f.snapshot(t); after != before {
				t.Fatalf("failed metadata changed before cleanup confirmed:\nbefore %s\nafter  %s", before, after)
			}
			var notices int
			if err := f.st.Read().QueryRow(`SELECT count(*) FROM events WHERE type='session.restarted'`).Scan(&notices); err != nil || notices != 0 {
				t.Fatalf("premature notice: %d %v", notices, err)
			}
			// No rollback is possible: a workspace already removed before stack failure
			// or an uncertain reply stays removed. The row/debt remains for owner retry.
			assertRestartFile(t, f.workspace, "old-work", mode != "stack" && mode != "uncertain")
			assertRestartFile(t, f.stack, "old-work", true)
			debts, err := f.st.Leftovers(context.Background(), "old")
			if err != nil || len(debts) != 1 {
				t.Fatalf("lost debt: %+v %v", debts, err)
			}
			if mode == "unreachable" {
				if raw, _ := os.ReadFile(f.log); len(raw) != 0 {
					t.Fatalf("unreachable host was used: %s", raw)
				}
				f.exec(t, `UPDATE hosts SET state='ready' WHERE name='old'`)
			}
			t.Setenv("RESTART_FAIL", "")
			if problem := f.restart(context.Background()); problem != nil {
				t.Fatalf("explicit retry: %v", problem)
			}
		})
	}
}

func TestSessionRestartBlockedCleanupDoesNotResetOrBlockOtherBoxes(t *testing.T) {
	f := newRestartFixture(t)
	t.Setenv("RESTART_GATE", "yes")
	before := f.snapshot(t)
	result := f.restartAsync(t, context.Background())
	waitPinnedFile(t, f.entered)
	if after := f.snapshot(t); after != before {
		t.Fatalf("premature reset: %s", after)
	}
	if problem := f.restart(context.Background()); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("duplicate restart: %v", problem)
	}
	f.exec(t, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('other','r','other','p','open','',1);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('other-box','other','','agent','pending','','',0,0);`)
	// Stop at repository lookup, independently proving admission/worker progress.
	repos := &restartBarrierRepos{}
	f.svc.SetRepositories(repos)
	pass := make(chan error, 1)
	go func() { pass <- f.svc.Reconciler().Pass(context.Background()) }()
	select {
	case err := <-pass:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restart holds the global pass lock across host I/O")
	}
	// The unrelated worker may already have failed and retried by this read.
	// Its typed creating event, unlike a transient state, proves admission while cleanup is blocked.
	var admitted bool
	if err := f.st.Read().QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM events WHERE type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')='other-box' AND json_extract(CAST(body AS TEXT),'$.to')='creating')`).Scan(&admitted); err != nil || !admitted {
		t.Fatalf("unrelated box was not admitted: %v", err)
	}
	if after := f.snapshot(t); after != before {
		t.Fatalf("Pass changed fenced box: %s", after)
	}
	if err := os.WriteFile(f.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if problem := <-result; problem != nil {
		t.Fatal(problem)
	}
	f.svc.Reconciler().Wait()
	if !repos.entered {
		t.Fatal("unrelated worker did not progress")
	}
}

func TestSessionRestartCancelledCleanupDoesNotReset(t *testing.T) {
	f := newRestartFixture(t)
	t.Setenv("RESTART_GATE", "yes")
	before := f.snapshot(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := f.restartAsync(t, ctx)
	waitPinnedFile(t, f.entered)
	cancel()
	if err := os.WriteFile(f.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case problem := <-result:
		if problem == nil || problem.Code != contract.ErrUnavailable {
			t.Fatalf("uncertain cancelled cleanup: %v", problem)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled cleanup did not return")
	}
	if after := f.snapshot(t); after != before {
		t.Fatalf("cancelled cleanup reset metadata: %s", after)
	}
}

func TestSessionRestartCloseWinsDuringCleanup(t *testing.T) {
	f := newRestartFixture(t)
	t.Setenv("RESTART_GATE", "yes")
	result := f.restartAsync(t, context.Background())
	waitPinnedFile(t, f.entered)
	// A concurrent close keeps its intent; completed cleanup cannot revive it.
	f.exec(t, `UPDATE boxes SET want='close' WHERE id='b'; UPDATE sessions SET state='closing' WHERE id='s'`)
	if err := os.WriteFile(f.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if problem := <-result; problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("close lost: %v", problem)
	}
	b, err := f.st.Box(context.Background(), "b")
	if err != nil || b.State != contract.BoxFailed || b.Image != "used-image" {
		t.Fatalf("close metadata: %+v %v", b, err)
	}
}
