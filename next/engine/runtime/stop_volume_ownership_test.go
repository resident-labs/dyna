package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

func unusedStopFixture(t *testing.T) *restartFixture {
	t.Helper()
	f := newRestartFixture(t)
	f.exec(t, `DELETE FROM turns; UPDATE boxes SET state='starting',want='stop',image='first-image',due_at=0 WHERE id='b'`)
	if err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.st.AppendEvent(tx, contract.Event{ID: "unused-initial", Room: "r", Time: f.now.Add(-time.Minute), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// Gate the actual named-volume command, after proof/claim and after runnable
// resources are gone. Unlike a second proof read, ownership must still prevent use.
func stopVolumeGate(t *testing.T, f *restartFixture, phase string) (string, string) {
	t.Helper()
	entered, release := filepath.Join(f.root, "volume-entered"), filepath.Join(f.root, "volume-release")
	t.Setenv("STOP_VOLUME_PHASE", phase)
	t.Setenv("STOP_VOLUME_ENTERED", entered)
	t.Setenv("STOP_VOLUME_RELEASE", release)
	t.Setenv("STOP_VOLUME_SCOPE", f.workspace)
	t.Setenv("STOP_VOLUME_FINISHED", filepath.Join(f.root, "volume-finished"))
	path := filepath.Join(strings.SplitN(os.Getenv("PATH"), ":", 2)[0], "podman")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gate := `gated_volume=no
if [[ " $* " == *" volume $STOP_VOLUME_PHASE "* ]] && [ "$RESTART_WORKSPACE" = "$STOP_VOLUME_SCOPE" ]; then
 gated_volume=yes
 : > "$STOP_VOLUME_ENTERED"
 while [ ! -f "$STOP_VOLUME_RELEASE" ]; do sleep 0.01; done
fi
`
	next := strings.Replace(string(raw), `case " $* " in`, gate+`case " $* " in`, 1)
	if next == string(raw) {
		t.Fatal("named-volume fixture anchor missing")
	}
	next += "\nif [ \"$gated_volume\" = yes ]; then : > \"$STOP_VOLUME_FINISHED\"; fi\n"
	if err := os.WriteFile(path, []byte(next), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	return entered, release
}

func assertVolumeOwnerFencesUse(t *testing.T, f *restartFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.st.WakeWithNotice(ctx, contract.Event{ID: "owned-volume-wake", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: "s", Text: "input while physical deletion owns the namespace"}}, "s"); err != nil {
		t.Fatal(err)
	}
	// Use the actual hello caller, not merely an isolated policy boolean. Observe
	// a window, so a prematurely released asynchronous keeper cannot pass merely
	// because its release goroutine had not yet run at the first hello.
	for range 5 {
		_, _ = f.svc.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, contract.BoxHelloRequest{})
		current, err := f.st.Box(ctx, "b")
		if err != nil {
			t.Fatal(err)
		}
		if current.State == contract.BoxReady {
			t.Fatal("BoxHello admitted readiness during physical volume removal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := f.st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.State = contract.BoxReady
	updated, err := f.st.SetBoxLifecycle(ctx, current, next, time.Time{}, 0, contract.Event{ID: "owned-volume-inventory-ready", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}})
	if err != nil || updated {
		t.Fatalf("inventory admitted readiness during physical volume removal: updated=%v err=%v", updated, err)
	}
	active, inputs, _, err := f.st.Dispatch(ctx, "s", "too-early", contract.Event{ID: "owned-volume-early-turn", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
	if err != nil || active != "" || len(inputs) != 0 {
		t.Fatalf("dispatch admitted work during physical volume removal: %s %v %v", active, inputs, err)
	}
	var want string
	if err := f.st.Read().QueryRowContext(ctx, `SELECT want FROM boxes WHERE id='b'`).Scan(&want); err != nil || want != "run" {
		t.Fatalf("ordinary wake was lost: want=%s err=%v", want, err)
	}
}

func admitOtherWhileVolumeOwned(t *testing.T, f *restartFixture) *restartBarrierRepos {
	t.Helper()
	f.exec(t, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('other','r','other','p','open','',1);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('other-box','other','','agent','pending','','',0,0);`)
	repos := &restartBarrierRepos{}
	f.svc.SetRepositories(repos)
	done := make(chan error, 1)
	go func() { done <- f.svc.Reconciler().Pass(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("physical volume ownership blocked unrelated reconciliation")
	}
	var admitted bool
	if err := f.st.Read().QueryRow(`SELECT EXISTS(SELECT 1 FROM events WHERE type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')='other-box' AND json_extract(CAST(body AS TEXT),'$.to')='creating')`).Scan(&admitted); err != nil || !admitted {
		t.Fatalf("unrelated allocation did not progress: %v", err)
	}
	return repos
}

func TestStopVolumeOwnershipSpansBothPhysicalDeletions(t *testing.T) {
	for _, phase := range []string{"rm", "prune"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%v", phase, fail), func(t *testing.T) {
				f := unusedStopFixture(t)
				entered, release := stopVolumeGate(t, f, phase)
				if fail {
					failure := "workspace"
					if phase == "prune" {
						failure = "stack"
					}
					t.Setenv("RESTART_FAIL", failure)
				}
				if err := f.svc.Reconciler().Pass(context.Background()); err != nil {
					t.Fatal(err)
				}
				waitPinnedFile(t, entered)
				assertVolumeOwnerFencesUse(t, f)
				repos := admitOtherWhileVolumeOwned(t, f)
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				f.svc.Reconciler().Wait()
				if !repos.entered {
					t.Fatal("unrelated worker never ran")
				}
				if err := f.st.JoinBoxVolumeRemovals(context.Background(), "b"); err != nil {
					t.Fatal(err)
				}
				if !fail { // A saved Image is not evidence of use: unused work is still discarded.
					assertRestartFile(t, f.workspace, "old-work", false)
					assertRestartFile(t, f.stack, "old-work", false)
				}
				current, err := f.st.Box(context.Background(), "b")
				if err != nil {
					t.Fatal(err)
				}
				next := current
				next.State, next.Image = contract.BoxStarting, "fresh-image"
				changed, err := f.st.SetBoxLifecycle(context.Background(), current, next, time.Time{}, 0, contract.Event{ID: "after-volume-start", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}})
				if err != nil || !changed {
					t.Fatalf("wake could not resume after physical owner finished: %v %v", changed, err)
				}
				if _, err := f.st.TransitionBox(context.Background(), next, contract.BoxReady, "", contract.Event{ID: "after-volume-ready", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}); err != nil {
					t.Fatal(err)
				}
				active, inputs, _, err := f.st.Dispatch(context.Background(), "s", "after-volume-turn", contract.Event{ID: "after-volume-turn-event", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
				if err != nil || active != "after-volume-turn" || len(inputs) == 0 {
					t.Fatalf("wake did not dispatch after physical ownership ended: %s %v %v", active, inputs, err)
				}
				for _, dir := range []string{f.workspace, f.stack} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "new-used-work"), []byte("first use after ownership release"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				// Later recovery with the same ID must not inherit the earlier Stop grant.
				if err := f.runtime.Remove(context.Background(), "b"); err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{f.workspace, f.stack} {
					assertRestartFile(t, dir, "new-used-work", true)
				}
			})
		}
	}
}

type volumeCancelRunner struct {
	fail  atomic.Bool
	scope *priorRestartRunner
}

func (r *volumeCancelRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if strings.Contains(args[len(args)-1], "dyna-cancel") && r.fail.Load() {
		return nil, errors.New("independent cancellation unavailable")
	}
	if r.scope != nil {
		args = append([]string(nil), args...)
		s := r.scope
		args[len(args)-1] = fmt.Sprintf("export RESTART_WORKSPACE=%q RESTART_STACK=%q RESTART_LOG=%q; export RESTART_FAIL=$(cat %q); ", s.workspace, s.stack, s.log, s.fail) + args[len(args)-1]
	}
	return (&nativePinnedRunner{}).Run(ctx, input, args...)
}

func TestUnconfirmedVolumeRemovalKeepsFenceAfterBoundedCaller(t *testing.T) {
	for _, target := range []contract.HostName{"old", "prior"} {
		t.Run(string(target), func(t *testing.T) {
			f := unusedStopFixture(t)
			entered, release := stopVolumeGate(t, f, "rm")
			runner := &volumeCancelRunner{}
			runner.fail.Store(true)
			h, err := podman.New("user@host", t.TempDir(), string(target), runner, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			f.runtime.hosts[target] = h
			f.runtime.retryDelay = 5 * time.Millisecond
			if target == "prior" {
				f.exec(t, `INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('prior','','ready',64,137438953472,1099511627776,0); INSERT INTO room_leftovers(host,room,box,slice) VALUES('prior','r','b','');`)
			}
			t.Cleanup(func() {
				runner.fail.Store(false)
				_ = os.WriteFile(release, nil, 0600)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = f.st.JoinBoxVolumeRemovals(ctx, "b")
			})
			work, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := podman.WithRemoteCancellationDeadline(work, time.Now().Add(5*time.Second))
			result := make(chan error, 1)
			go func() {
				if target == "old" {
					result <- f.runtime.Remove(ctx, "b")
				} else {
					_, err := f.runtime.RemoveLeftoverBox(ctx, target, "b", "")
					result <- err
				}
			}()
			waitPinnedFile(t, entered)
			canceled := time.Now()
			cancel()
			select {
			case err := <-result:
				var uncertain podman.RemoteCancellationUnconfirmed
				if !errors.As(err, &uncertain) {
					t.Fatalf("bounded caller lost typed uncertainty: %v", err)
				}
				if time.Since(canceled) > 3*time.Second {
					t.Fatal("caller waited for the remote keeper beyond its join budget")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("caller blocked on unconfirmed physical deletion")
			}
			assertVolumeOwnerFencesUse(t, f)
			// A failed row can be exposed after the bounded step returns. The actual
			// owner restart must join the surviving physical owner, including prior hosts.
			current, err := f.st.Box(context.Background(), "b")
			if err != nil {
				t.Fatal(err)
			}
			failed := current
			failed.State, failed.Reason = contract.BoxFailed, "cleanup could not join"
			if ok, err := f.st.SetBoxLifecycle(context.Background(), current, failed, time.Time{}, 0, contract.Event{ID: "unconfirmed-volume-failure", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Cause: contract.CauseStartFailed}}); err != nil || !ok {
				t.Fatalf("failed step: %v %v", ok, err)
			}
			before := f.snapshot(t)
			restartCtx, stop := context.WithTimeout(context.Background(), 350*time.Millisecond)
			restartStarted := time.Now()
			restart := f.restartAsync(t, restartCtx)
			repos := admitOtherWhileVolumeOwned(t, f)
			problem := <-restart
			stop()
			if time.Since(restartStarted) < 250*time.Millisecond {
				t.Fatal("owner restart bypassed the surviving physical volume owner")
			}
			if problem == nil || !strings.Contains(problem.Message, "host "+string(target)) {
				t.Fatalf("restart did not join/name actual volume keeper %s: %v", target, problem)
			}
			if target == "prior" && strings.Contains(problem.Message, "host old") {
				t.Fatalf("prior keeper mislabeled as current host: %v", problem)
			}
			if after := f.snapshot(t); after != before {
				t.Fatalf("restart reset before physical join: %s", after)
			}
			runner.fail.Store(false) // Permit independent kill/join, not a fabricated completion bit.
			joined, stopJoin := context.WithTimeout(context.Background(), 5*time.Second)
			if err := f.st.JoinBoxVolumeRemovals(joined, "b"); err != nil {
				stopJoin()
				t.Fatal(err)
			}
			stopJoin()
			f.svc.Reconciler().Wait()
			if !repos.entered {
				t.Fatal("unrelated worker did not progress while the keeper was pending")
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if problem := f.restart(context.Background()); problem != nil {
				t.Fatalf("owner retry after physical join: %v", problem)
			}
		})
	}
}
