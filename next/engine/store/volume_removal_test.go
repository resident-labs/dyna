package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestVolumeRemovalJoinsRemainingTypedHostsAndFencesReset(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET want='stop',state='failed',reason='old failure' WHERE id=?`, b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	old, err := r.s.Box(r.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, prior := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-first:
		default:
			close(first)
		}
		select {
		case <-prior:
		default:
			close(prior)
		}
	})
	if err := r.s.AddBoxLeftover(r.ctx, "spare", "r", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("physical group remains unconfirmed")
	for _, claim := range []struct {
		host contract.HostName
		done <-chan struct{}
	}{{"arch", first}, {"spare", prior}} {
		removed, err := r.s.RemoveBoxVolumes(r.ctx, b.ID, claim.host, func(context.Context) error { return VolumeRemovalPending{Err: sentinel, Joined: claim.done} })
		if removed || !errors.Is(err, sentinel) {
			t.Fatalf("unconfirmed claim reported completion: %v %v", removed, err)
		}
	}
	// Isolate the physical-owner reset guard from the independent debt CAS.
	// The prior target was recorded when its operation acquired ownership.
	if err := r.s.ForgetLeftover(r.ctx, Leftover{Host: "spare", Room: "r", Box: b.ID}); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "volume-reset", Room: "r", Time: r.now.Add(time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	notice := contract.Event{ID: "volume-reset-notice", Room: "r", Time: r.now.Add(time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: b.Session}}
	if reset, err := r.s.RestartSession(r.ctx, old, event, notice); err != nil || reset {
		t.Fatalf("reset committed before physical volume joins: %v %v", reset, err)
	}
	close(first)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var remaining bool
		if err := r.s.Write(r.ctx, func(*sql.Tx) error {
			for claim := range r.s.volumeRemovals[b.ID] {
				if claim.host == "arch" {
					remaining = true
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !remaining {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirmed first host kept its ownership")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Millisecond)
	err = r.s.JoinBoxVolumeRemovals(ctx, b.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "spare") || strings.Contains(err.Error(), "arch") {
		t.Fatalf("join did not name only the actual outstanding host: %v", err)
	}
	close(prior)
	joined, cancel := context.WithTimeout(r.ctx, 2*time.Second)
	defer cancel()
	if err := r.s.JoinBoxVolumeRemovals(joined, b.ID); err != nil {
		t.Fatal(err)
	}
	if reset, err := r.s.RestartSession(r.ctx, old, event, notice); err != nil || !reset {
		t.Fatalf("confirmed namespace could not reset: %v %v", reset, err)
	}
}

func TestVolumeRemovalCancellationReleasesOnlyJoinedOwnership(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE boxes SET want='stop' WHERE id=?`, b.ID); return err }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(r.ctx)
	removed, err := r.s.RemoveBoxVolumes(ctx, b.ID, "arch", func(ctx context.Context) error { cancel(); return ctx.Err() })
	if removed || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled joined removal: %v %v", removed, err)
	}
	joined, stop := context.WithTimeout(r.ctx, time.Second)
	defer stop()
	if err := r.s.JoinBoxVolumeRemovals(joined, b.ID); err != nil {
		t.Fatalf("joined cancellation leaked ownership: %v", err)
	}
	called := false
	removed, err = r.s.RemoveBoxVolumes(ctx, b.ID, "arch", func(context.Context) error { called = true; return nil })
	if removed || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("already canceled caller entered physical deletion: %v %v %v", removed, err, called)
	}
	if err := r.s.JoinBoxVolumeRemovals(joined, b.ID); err != nil {
		t.Fatalf("canceled acquisition leaked ownership: %v", err)
	}
}

func TestVolumeRemovalFencesAllAdmissionWritersUntilPhysicalJoin(t *testing.T) {
	for _, writer := range []string{"transition", "lifecycle"} {
		t.Run(writer, func(t *testing.T) {
			r, b := newCreateRetryRig(t, new(contract.HostName))
			if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE boxes SET state='stopped',want='stop',image='' WHERE id=?`, b.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			joined := make(chan struct{})
			defer close(joined)
			_, _ = r.s.RemoveBoxVolumes(r.ctx, b.ID, "arch", func(context.Context) error {
				return VolumeRemovalPending{Err: errors.New("blocked physical mutation"), Joined: joined}
			})
			if err := r.s.WakeWithNotice(r.ctx, contract.Event{ID: "admission-volume-wake", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: b.Session, Text: "wake must remain queued"}}, b.Session); err != nil {
				t.Fatal(err)
			}
			old, err := r.s.Box(r.ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			event := contract.Event{ID: "admission-volume-start", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
			if writer == "transition" {
				if _, err := r.s.TransitionBox(r.ctx, old, contract.BoxCreating, "", event); !errors.Is(err, ErrNotFound) {
					t.Fatalf("transition admitted fresh namespace while deletion remained unconfirmed: %v", err)
				}
			} else {
				next := old
				next.State = contract.BoxCreating
				if ok, err := r.s.SetBoxLifecycle(r.ctx, old, next, time.Time{}, 0, event); err != nil || ok {
					t.Fatalf("lifecycle admitted fresh namespace while deletion remained unconfirmed: %v %v", ok, err)
				}
			}
		})
	}
}
