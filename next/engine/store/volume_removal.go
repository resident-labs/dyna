package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Each pointer owns one physical operation, not a box incarnation or deletion
// permission. Exact identity makes late keeper completion harmless after an
// explicit host abandonment, even if another operation now owns the same names.
type boxVolumeRemoval struct {
	host contract.HostName
	done chan struct{}
}

// VolumeRemovalPending keeps physical ownership after a bounded caller returns.
// Joined closes only after its engine-owned keeper confirms the remote group
// joined. Store closes optional Retired when ownership ends, including explicit
// successful gone-for-good host abandonment, so the keeper can stop retrying.
// Unwrap preserves the transport's typed uncertainty at existing callers.
type VolumeRemovalPending struct {
	Err     error
	Joined  <-chan struct{}
	Retired chan<- struct{}
}

func (e VolumeRemovalPending) Error() string { return e.Err.Error() }
func (e VolumeRemovalPending) Unwrap() error { return e.Err }

// RemoveBoxVolumes claims the namespace in the SAME serialized write as the fresh
// target reference and deletion proof, then runs remove outside the store writer
// and reconciler pass. The target must still be current or recorded debt, so a
// worker captured before host abandonment cannot reopen that host's ownership.
// Wakes remain accepted, but admission, readiness and turn dispatch cannot race
// physical deletion. A policy read alone (CanRemoveBoxVolumes) owns nothing.
//
// remove must join its physical mutations before returning, or return
// VolumeRemovalPending with a keeper's confirmation channel. Unconfirmed I/O must
// not release ownership just because the caller's work/join budget expired.
func (s *Store) RemoveBoxVolumes(ctx context.Context, id contract.BoxID, host contract.HostName, remove func(context.Context) error) (bool, error) {
	var claim *boxVolumeRemoval
	release := func() error {
		return s.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
			return s.retireBoxVolumeRemoval(context.WithoutCancel(ctx), tx, id, claim)
		})
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM boxes WHERE id=? AND host=?) OR EXISTS(SELECT 1 FROM room_leftovers WHERE box=? AND host=?)`, id, host, id, host).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return nil
		}
		allowed, err := canRemoveBoxVolumes(ctx, tx, id)
		if err != nil || !allowed {
			return err
		}
		if s.volumeRemovals == nil {
			s.volumeRemovals = make(map[contract.BoxID]map[*boxVolumeRemoval]struct{})
		}
		if s.volumeRemovals[id] == nil {
			s.volumeRemovals[id] = make(map[*boxVolumeRemoval]struct{})
		}
		claim = &boxVolumeRemoval{host: host, done: make(chan struct{})}
		s.volumeRemovals[id][claim] = struct{}{}
		return nil
	})
	if err != nil {
		if claim != nil {
			err = errors.Join(err, release())
		}
		return false, err
	}
	if claim == nil {
		return false, nil
	}
	err = remove(ctx)
	var pending VolumeRemovalPending
	if errors.As(err, &pending) {
		go func() {
			if pending.Retired != nil {
				defer close(pending.Retired)
			}
			select {
			case <-pending.Joined:
				_ = release() // A closed Store can no longer process this retirement.
			case <-claim.done: // Explicit abandonment already retired this exact operation.
			case <-s.Done():
			}
		}()
		return false, err
	}
	releaseErr := release()
	return err == nil && releaseErr == nil, errors.Join(err, releaseErr)
}

// retireBoxVolumeRemoval runs only in Write, after physical join or after a
// committed explicit host release. An old operation cannot release a newer one.
func (s *Store) retireBoxVolumeRemoval(ctx context.Context, tx *sql.Tx, id contract.BoxID, claim *boxVolumeRemoval) error {
	if _, owned := s.volumeRemovals[id][claim]; !owned {
		return nil
	}
	delete(s.volumeRemovals[id], claim)
	if len(s.volumeRemovals[id]) == 0 {
		delete(s.volumeRemovals, id)
	}
	close(claim.done)
	// Retry an accepted wake whose admission/dispatch met the fence. Its delivery
	// was never consumed. No event or deletion permission is stored here.
	var session contract.SessionID
	err := tx.QueryRowContext(ctx, `SELECT session FROM boxes WHERE id=?`, id).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err == nil {
		s.wakePending = append(s.wakePending, session)
	}
	return err
}

// JoinBoxVolumeRemovals joins ownership left behind by bounded callers. A
// successful explicit host abandonment may also retire its matching ownership.
// Capture under the writer, wait outside it. Lifecycle restart first fences and
// joins same-ID workers so none can claim after this snapshot; commit also checks.
func (s *Store) JoinBoxVolumeRemovals(ctx context.Context, id contract.BoxID) error {
	var claims []*boxVolumeRemoval
	var hosts []string
	capture := func(ctx context.Context) error {
		return s.Write(ctx, func(*sql.Tx) error {
			claims, hosts = nil, nil
			for claim := range s.volumeRemovals[id] {
				claims = append(claims, claim)
				hosts = append(hosts, string(claim.host))
			}
			slices.Sort(hosts)
			hosts = slices.Compact(hosts)
			return nil
		})
	}
	if err := capture(ctx); err != nil {
		return err
	}
	for _, claim := range claims {
		select {
		case <-claim.done:
			continue
		default:
		}
		select {
		case <-claim.done:
		case <-ctx.Done():
			// Name only the operations still outstanding, not the failed row's
			// possibly unrelated current host or a host already confirmed/abandoned.
			if err := capture(context.WithoutCancel(ctx)); err != nil {
				return errors.Join(ctx.Err(), err)
			}
			if len(claims) == 0 {
				return nil
			}
			return fmt.Errorf("volume removal join on host %s: %w", strings.Join(hosts, ", "), ctx.Err())
		case <-s.Done():
			return fmt.Errorf("store closed before volume removal joined on host %s", strings.Join(hosts, ", "))
		}
	}
	return nil
}

// volumeRemovalHostHeld is part of ordinary empty-host removal, never an
// authorization to abandon. Explicit discard reaches ReleaseHostBoxes instead.
func (s *Store) volumeRemovalHostHeld(host contract.HostName) error {
	var held []string
	for id, claims := range s.volumeRemovals {
		for claim := range claims {
			if claim.host == host {
				held = append(held, "volume cleanup for box "+string(id))
				break
			}
		}
	}
	if len(held) == 0 {
		return nil
	}
	slices.Sort(held)
	return &HostHeldError{Host: host, Held: held}
}
