package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type imageWakeHost struct {
	dormantHost
	image           string
	err             error
	recreated       contract.BoxID
	tag             string
	slice, oldSlice string
	legacy          bool
}

func (h *imageWakeHost) Inspect(_ context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{Box: id, StaleLayout: h.legacy}, true, nil
}

func (h *imageWakeHost) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: h.image}, h.err
}
func (h *imageWakeHost) Recreate(_ context.Context, spec BoxSpec, image, oldSlice string) error {
	h.recreated = spec.Box
	h.tag = image
	h.slice, h.oldSlice = spec.Slice, oldSlice
	h.legacy = false
	return nil
}

// LEGACY(v1-transfer): this test, a box on an old nested parent slice. Remove after the transfer.
func TestWakeFlattensLegacySlice(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 0)
	h := &imageWakeHost{image: "image"}
	s.boxes = h
	resetLifecycle(s)
	passLifecycle(t, s, ctx)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	const oldSlice = "dyna-old-child.slice"
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET slice=? WHERE id=?", oldSlice, session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	want, err := box.SliceName(session.Box)
	if err != nil || h.oldSlice != oldSlice || h.slice != want || b.Slice != want || h.starts != 0 {
		t.Fatalf("old=%q new=%q box=%q starts=%d err=%v", h.oldSlice, h.slice, b.Slice, h.starts, err)
	}
}

func TestWakeUsesCurrentImage(t *testing.T) {
	for _, tc := range []struct {
		name, image string
		err         error
		recreate    bool
		legacy      bool
	}{
		{name: "new", image: "new", recreate: true}, {name: "current", image: "image"}, {name: "shared mount upgrade", image: "image", legacy: true, recreate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, room, sid, ctx := quietSession(t, 0)
			h := &imageWakeHost{image: tc.image, err: tc.err, legacy: tc.legacy}
			s.boxes = h
			resetLifecycle(s)
			passLifecycle(t, s, ctx)
			wakeSession(t, s, ctx, room, sid)
			passLifecycle(t, s, ctx)
			session, err := s.store.Session(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.store.Box(ctx, session.Box)
			if err != nil {
				t.Fatal(err)
			}
			if tc.recreate {
				if h.recreated != b.ID || h.tag != tc.image || b.Image != tc.image || h.starts != 0 {
					t.Fatalf("recreate=%s tag=%s box=%+v starts=%d", h.recreated, h.tag, b, h.starts)
				}
			} else if h.recreated != "" || h.starts != 1 || b.Image != "image" {
				t.Fatalf("recreate=%s starts=%d image=%s", h.recreated, h.starts, b.Image)
			}
			var notices int
			if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='session.restarted' AND d.state='pending' AND d.wake=0", sid).Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{true: 1, false: 0}[tc.image == "new"]; notices != want {
				t.Fatalf("notices=%d, want %d", notices, want)
			}
		})
	}
}

// The box agent comes from the host, so an old image could hold a Pi it cannot drive. The box retries, then fails as any
// box does that cannot start, and it never runs the old image.
func TestWakeFailsWhenTheCurrentImageCannotBeBuilt(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 0)
	h := &imageWakeHost{image: "image"}
	s.boxes = h
	resetLifecycle(s)
	passLifecycle(t, s, ctx)
	h.err = errors.New("build failed")
	wakeSession(t, s, ctx, room, sid)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	var b box.Box
	for range 3 {
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE boxes SET due_at=0 WHERE id=?", session.Box)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		passLifecycle(t, s, ctx)
		if b, err = s.store.Box(ctx, session.Box); err != nil {
			t.Fatal(err)
		}
	}
	if h.starts != 0 || h.recreated != "" || b.State != contract.BoxStopped || b.Reason != "build failed" || b.Image != "image" {
		t.Fatalf("starts=%d recreated=%s box=%+v", h.starts, h.recreated, b)
	}
	var cause string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT coalesce(json_extract(body,'$.cause'),'') FROM events WHERE type='box.changed' ORDER BY seq DESC LIMIT 1").Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if cause != string(contract.CauseStartFailed) {
		t.Fatalf("cause=%q", cause)
	}
}
