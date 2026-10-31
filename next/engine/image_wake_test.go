package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type imageWakeHost struct {
	dormantHost
	image     string
	err       error
	recreated contract.BoxID
	tag       string
}

func (h *imageWakeHost) CurrentImage(context.Context, contract.SessionID, contract.HostName) (string, error) {
	return h.image, h.err
}
func (h *imageWakeHost) Recreate(_ context.Context, spec BoxSpec, image, _ string) error {
	h.recreated = spec.Box
	h.tag = image
	return nil
}

func TestWakeUsesCurrentImage(t *testing.T) {
	for _, tc := range []struct {
		name, image string
		err         error
		recreate    bool
	}{
		{name: "new", image: "new", recreate: true}, {name: "current", image: "image"}, {name: "build fails", err: errors.New("build failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, room, sid, ctx := quietSession(t, 0)
			h := &imageWakeHost{image: tc.image, err: tc.err}
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
			if tc.err != nil && b.Reason != tc.err.Error() {
				t.Fatalf("reason=%q", b.Reason)
			}
		})
	}
}
