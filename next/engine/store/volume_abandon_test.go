package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func pendingVolumeClaim(t *testing.T, s *Store, id contract.BoxID, host contract.HostName) chan struct{} {
	t.Helper()
	joined := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-joined:
		default:
			close(joined)
		}
	})
	removed, err := s.RemoveBoxVolumes(context.Background(), id, host, func(context.Context) error {
		return VolumeRemovalPending{Err: errors.New("unconfirmed physical fixture"), Joined: joined}
	})
	if removed || err == nil {
		t.Fatalf("pending volume claim: %v %v", removed, err)
	}
	return joined
}

func drainForVolumeDiscard(t *testing.T, r *failureRig, host contract.HostName) {
	t.Helper()
	_, err := r.s.DiscardHostBoxes(r.ctx, host, func(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
		r.events++
		return contract.Event{ID: contract.EventID(fmt.Sprintf("discard-volume-%d", r.events)), Room: room, Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func expectVolumeJoinHosts(t *testing.T, s *Store, id contract.BoxID, want, not string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := s.JoinBoxVolumeRemovals(ctx, id)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "host "+want) || not != "" && strings.Contains(err.Error(), not) {
		t.Fatalf("physical ownership changed without the matching release: wanted %s, not %s: %v", want, not, err)
	}
}

func TestOnlyCommittedExplicitHostDiscardRetiresMatchingVolumeClaims(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE boxes SET want='stop' WHERE id=?`, b.ID); return err }); err != nil {
		t.Fatal(err)
	}
	for _, host := range []contract.HostName{"arch", "spare"} {
		if err := r.s.AddBoxLeftover(r.ctx, host, "r", b.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	current := pendingVolumeClaim(t, r.s, b.ID, "arch")
	prior := pendingVolumeClaim(t, r.s, b.ID, "spare")
	// An invalid/unvalidated release, ordinary reachability loss and degraded
	// check results cannot supply the explicit abandonment boundary.
	if err := r.s.ReleaseHostBoxes(r.ctx, "spare"); err == nil {
		t.Fatal("unvalidated host release was accepted")
	}
	for range 3 {
		if err := r.s.RecordHostHeartbeat(r.ctx, "spare", false, nil, 0, r.now); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.s.SetHostChecked(r.ctx, box.Host{Name: "spare", State: box.HostUnreachable, Health: contract.HostDegraded, Problem: contract.HostProblemShareUnavailable, CheckedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []func(context.Context, contract.HostName) error{r.s.DrainHost, r.s.RemoveHost} {
		var held *HostHeldError
		if err := remove(r.ctx, "spare"); !errors.As(err, &held) {
			t.Fatalf("plain removal did not retain prior-host physical ownership: %v", err)
		}
	}
	expectVolumeJoinHosts(t, r.s, b.ID, "arch, spare", "")
	drainForVolumeDiscard(t, r, "spare")
	expectVolumeJoinHosts(t, r.s, b.ID, "arch, spare", "") // Validation/draining is not the release.
	if err := r.s.ReleaseHostBoxes(r.ctx, "spare"); err != nil {
		t.Fatal(err)
	}
	expectVolumeJoinHosts(t, r.s, b.ID, "arch", "spare")
	after, err := r.s.Box(r.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Host != "arch" || after.State != b.State {
		t.Fatalf("prior-only discard changed the current allocation: %+v", after)
	}
	debts, err := r.s.BoxLeftovers(r.ctx, b.ID)
	if err != nil || len(debts) != 1 || debts[0].Host != "arch" {
		t.Fatalf("discard retired unrelated-host debt or retained its own: %+v %v", debts, err)
	}
	close(prior) // A late keeper acknowledgment has no authority over arch.
	expectVolumeJoinHosts(t, r.s, b.ID, "arch", "spare")
	close(current)
	ctx, cancel := context.WithTimeout(r.ctx, time.Second)
	defer cancel()
	if err := r.s.JoinBoxVolumeRemovals(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFailedExplicitHostReleaseKeepsPhysicalClaimsAndDebt(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE boxes SET want='stop' WHERE id=?`, b.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if err := r.s.AddBoxLeftover(r.ctx, "spare", "r", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	_ = pendingVolumeClaim(t, r.s, b.ID, "spare")
	drainForVolumeDiscard(t, r, "spare")
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER reject_volume_release BEFORE DELETE ON room_leftovers WHEN OLD.host='spare' BEGIN SELECT RAISE(ABORT,'injected release failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.ReleaseHostBoxes(r.ctx, "spare"); err == nil {
		t.Fatal("injected host release unexpectedly succeeded")
	}
	expectVolumeJoinHosts(t, r.s, b.ID, "spare", "")
	debts, err := r.s.BoxLeftovers(r.ctx, b.ID)
	if err != nil || len(debts) != 1 || debts[0].Host != "spare" {
		t.Fatalf("failed release lost durable debt: %+v %v", debts, err)
	}
}

func TestAbandonedVolumeOperationCannotReleaseLaterOrUnrelatedClaims(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE boxes SET want='stop' WHERE id=?`, b.ID); return err }); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := r.s.RemoveBoxVolumes(r.ctx, b.ID, "arch", func(context.Context) error { close(entered); <-finish; return nil })
		result <- err
	}()
	<-entered
	t.Cleanup(func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	})
	if err := r.s.AddBoxLeftover(r.ctx, "spare", "r", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	unrelated := pendingVolumeClaim(t, r.s, b.ID, "spare")
	drainForVolumeDiscard(t, r, "arch")
	if err := r.s.ReleaseHostBoxes(r.ctx, "arch"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.RemoveHost(r.ctx, "arch"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.AddHost(r.ctx, box.Host{Name: "arch", SSH: "different-machine", State: box.HostReady, CheckedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.AddBoxLeftover(r.ctx, "arch", "r", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	later := pendingVolumeClaim(t, r.s, b.ID, "arch")
	close(finish)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	expectVolumeJoinHosts(t, r.s, b.ID, "arch, spare", "")
	close(later)
	// The new arch operation may release; the unrelated host must still own its
	// namespace. The old arch callback above was not its completion capability.
	deadline := time.Now().Add(time.Second)
	for {
		var arch bool
		if err := r.s.Write(r.ctx, func(*sql.Tx) error {
			for claim := range r.s.volumeRemovals[b.ID] {
				arch = arch || claim.host == "arch"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !arch {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("later confirmed claim did not release")
		}
		time.Sleep(time.Millisecond)
	}
	expectVolumeJoinHosts(t, r.s, b.ID, "spare", "arch")
	close(unrelated)
	ctx, cancel := context.WithTimeout(r.ctx, time.Second)
	defer cancel()
	if err := r.s.JoinBoxVolumeRemovals(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
}
