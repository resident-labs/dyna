package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

// Native ordinary cleanup is delayed at container Stop. After successful owner
// abandonment, model the SSH transport becoming unavailable at its next volume
// command. No real host or service is involved.
type lateHostVolumeTransport struct {
	gone  atomic.Bool
	scope *priorRestartRunner
}

func (r *lateHostVolumeTransport) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	command := args[len(args)-1]
	if r.gone.Load() && (strings.Contains(command, "dyna-cancel") || strings.Contains(command, "'volume' 'rm'")) {
		return nil, errors.New("abandoned host no longer answers")
	}
	if r.scope != nil {
		return r.scope.Run(ctx, input, args...)
	}
	return (&nativePinnedRunner{}).Run(ctx, input, args...)
}

func TestHostDiscardPreventsLateStopVolumeOwnership(t *testing.T) {
	for _, target := range []contract.HostName{"old", "prior"} {
		t.Run(string(target), func(t *testing.T) {
			f := unusedStopFixture(t)
			runner := &lateHostVolumeTransport{}
			if target == "prior" {
				work, stack, log, fail := f.priorHost(t)
				runner.scope = &priorRestartRunner{work, stack, log, fail}
				f.exec(t, `UPDATE boxes SET state='failed' WHERE id='b'; DELETE FROM room_leftovers WHERE host='old'`)
				f.svc.Reconciler().SetRoomHosts(f.runtime)
			}
			h, err := podman.New("user@local-fixture", t.TempDir(), string(target), runner, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			f.runtime.hosts[target] = h
			f.runtime.retryDelay = 5 * time.Millisecond
			t.Setenv("RESTART_GATE", "yes")
			t.Cleanup(func() {
				runner.gone.Store(false)
				_ = os.WriteFile(f.release, nil, 0600)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = f.st.JoinBoxVolumeRemovals(ctx, "b")
			})
			if err := f.svc.Reconciler().Pass(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitPinnedFile(t, f.entered) // Actual lifecycle Stop or actual room-debt repayment.
			if err := f.st.RecordHostHeartbeat(context.Background(), target, false, nil, 0, f.now); err != nil {
				t.Fatal(err)
			}
			_, p := f.svc.HostRemove(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostRemoveRequest{Host: target, DiscardBoxes: true})
			if p != nil {
				t.Fatal(p)
			}
			var remains bool
			if err := f.st.Read().QueryRow(`SELECT EXISTS(SELECT 1 FROM hosts WHERE name=?)`, target).Scan(&remains); err != nil || remains {
				t.Fatalf("explicit host abandonment did not finish: %v %v", remains, err)
			}
			// Release the already-running ordinary cleanup AFTER the successful release
			// snapshot. Its stale captured host must not gain a new same-ID ownership claim.
			runner.gone.Store(true)
			if err := os.WriteFile(f.release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			f.svc.Reconciler().Wait()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			p = f.restart(ctx)
			cancel()
			if p != nil {
				if p.Code != contract.ErrUnavailable || !strings.Contains(p.Message, "volume removal join on host "+string(target)) {
					t.Fatalf("unexpected restart failure, not the abandoned-host ownership finding: %v", p)
				}
				t.Fatalf("successful gone-for-good release was undone by stale ordinary cleanup; owner restart elsewhere is blocked: %v", p)
			}
		})
	}
}
