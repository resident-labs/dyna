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

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestRecoveryDoesNotInheritAnEarlierUnusedStopGrant(t *testing.T) {
	f := unusedStopFixture(t)
	allowed, err := f.st.CanRemoveBoxVolumes(context.Background(), "b")
	if err != nil || !allowed {
		t.Fatalf("unused Stop precondition: %v %v", allowed, err)
	}
	if err := f.st.WakeWithNotice(context.Background(), contract.Event{ID: "recover-without-stop", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: "s", Text: "recover normally"}}, "s"); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.Remove(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "old-work", true)
	}
}

func TestExplicitReachableHostDiscardErasesUsedRunWorkWithoutClosingSession(t *testing.T) {
	f := newRestartFixture(t)
	f.exec(t, `UPDATE boxes SET want='run' WHERE id='b'`)
	if _, problem := f.svc.HostRemove(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostRemoveRequest{Host: "old", DiscardBoxes: true}); problem != nil {
		t.Fatal(problem)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "old-work", false)
	}
	var state, goal, want string
	var closed sql.NullInt64
	if err := f.st.Read().QueryRow(`SELECT s.state,s.closed_at,s.goal,b.want FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id='s'`).Scan(&state, &closed, &goal, &want); err != nil {
		t.Fatal(err)
	}
	if state != "open" || closed.Valid || goal != "retained goal" || want != "run" {
		t.Fatalf("host discard closed or changed retained session: %s %v %q %s", state, closed, goal, want)
	}
	if problem := f.restart(context.Background()); problem != nil {
		t.Fatalf("retained session could not restart after explicit host discard: %v", problem)
	}
}

func TestExplicitGoneHostDiscardRetiresOnlyItsNativeVolumeOwner(t *testing.T) {
	for _, target := range []contract.HostName{"old", "prior"} {
		t.Run(string(target), func(t *testing.T) {
			f := unusedStopFixture(t)
			priorWork, priorStack, priorLog, priorFail := f.priorHost(t)
			targetWork, otherWork, otherStack := f.workspace, priorWork, priorStack
			otherHost := contract.HostName("prior")
			runner := &volumeCancelRunner{}
			if target == "prior" {
				targetWork, otherWork, otherStack = priorWork, f.workspace, f.stack
				otherHost = "old"
				runner.scope = &priorRestartRunner{priorWork, priorStack, priorLog, priorFail}
			}
			entered, release := stopVolumeGate(t, f, "rm")
			t.Setenv("STOP_VOLUME_SCOPE", targetWork)
			runner.fail.Store(true)
			h, err := podman.New("user@gone-host", t.TempDir(), string(target), runner)
			if err != nil {
				t.Fatal(err)
			}
			f.runtime.hosts[target] = h
			f.runtime.retryDelay = 5 * time.Millisecond
			otherJoined := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-otherJoined:
				default:
					close(otherJoined)
				}
				runner.fail.Store(false)
				_ = os.WriteFile(release, nil, 0600)
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
			// An unrelated same-ID physical owner must survive the target's discard.
			_, err = f.st.RemoveBoxVolumes(context.Background(), "b", otherHost, func(context.Context) error {
				return store.VolumeRemovalPending{Err: errors.New("other host group unconfirmed"), Joined: otherJoined}
			})
			if err == nil {
				t.Fatal("other-host ownership precondition failed")
			}
			cancel()
			select {
			case err := <-result:
				var uncertain podman.RemoteCancellationUnconfirmed
				if !errors.As(err, &uncertain) {
					t.Fatalf("native owner lost uncertainty: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("bounded native caller waited for its keeper")
			}
			assertVolumeOwnerFencesUse(t, f)
			if err := f.st.RecordHostHeartbeat(context.Background(), target, false, nil, 0, f.now); err != nil {
				t.Fatal(err)
			}
			if _, problem := f.svc.HostRemove(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostRemoveRequest{Host: target}); problem == nil || problem.Code != contract.ErrConflict {
				t.Fatalf("plain host removal abandoned cleanup: %v", problem)
			}
			if _, problem := f.svc.HostRemove(context.Background(), contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, contract.HostRemoveRequest{Host: target, DiscardBoxes: true}); problem == nil || problem.Code != contract.ErrForbidden {
				t.Fatalf("unvalidated discard was accepted: %v", problem)
			}
			if _, problem := f.svc.HostRemove(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostRemoveRequest{Host: target, DiscardBoxes: true}); problem != nil {
				t.Fatal(problem)
			}
			join, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
			err = f.st.JoinBoxVolumeRemovals(join, "b")
			stop()
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "host "+string(otherHost)) || strings.Contains(err.Error(), "host "+string(target)) {
				t.Fatalf("explicit discard retired the wrong host's ownership: %v", err)
			}
			debts, err := f.st.BoxLeftovers(context.Background(), "b")
			if err != nil || len(debts) != 1 || debts[0].Host != otherHost {
				t.Fatalf("explicit discard released the wrong host's debt: %+v %v", debts, err)
			}
			close(otherJoined)
			join, stop = context.WithTimeout(context.Background(), time.Second)
			if err := f.st.JoinBoxVolumeRemovals(join, "b"); err != nil {
				stop()
				t.Fatal(err)
			}
			stop()
			current, err := f.st.Box(context.Background(), "b")
			if err != nil {
				t.Fatal(err)
			}
			if target == "old" {
				if current.Host != "" || current.State != contract.BoxFailed {
					t.Fatalf("gone host retained its allocation: %+v", current)
				}
				if problem := f.restart(context.Background()); problem != nil {
					t.Fatalf("gone-for-good host's keeper blocked owner restart: %v", problem)
				}
				current, err = f.st.Box(context.Background(), "b")
				if err != nil {
					t.Fatal(err)
				}
				next := current
				next.State, next.Host = contract.BoxCreating, otherHost
				if ok, err := f.st.SetBoxLifecycle(context.Background(), current, next, time.Time{}, 0, contract.Event{ID: "after-abandon-create", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}); err != nil || !ok {
					t.Fatalf("fresh placement after explicit abandonment: %v %v", ok, err)
				}
				current = next
			} else if current.Host != otherHost || current.State != contract.BoxStarting {
				t.Fatalf("prior-only discard changed current allocation: %+v", current)
			}
			if _, err := f.st.TransitionBox(context.Background(), current, contract.BoxReady, "", contract.Event{ID: "after-abandon-ready", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}); err != nil {
				t.Fatal(err)
			}
			active, _, _, err := f.st.Dispatch(context.Background(), "s", "after-abandon-turn", contract.Event{ID: "after-abandon-turn-event", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
			if err != nil || active != "after-abandon-turn" {
				t.Fatalf("accepted wake could not dispatch after explicit release: %s %v", active, err)
			}
			for _, dir := range []string{otherWork, otherStack} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "new-used-work"), []byte("new work on the retained host"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// The old destructive command really survives its caller and now completes
			// on the explicitly abandoned host. Distinct host filesystems ensure it
			// cannot erase the retained/new host's work or release a different owner.
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitPinnedFile(t, filepath.Join(f.root, "volume-finished"))
			for _, dir := range []string{otherWork, otherStack} {
				assertRestartFile(t, dir, "new-used-work", true)
			}
			if err := f.st.JoinBoxVolumeRemovals(context.Background(), "b"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
