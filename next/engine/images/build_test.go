package images

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type fakeHost struct {
	calls int
	err   error
}

func (h *fakeHost) Build(context.Context, string, string, []byte) error { h.calls++; return h.err }

type fakeRecorder struct{ body contract.EventBody }

func (r *fakeRecorder) Record(_ context.Context, _ contract.RoomID, body contract.EventBody) error {
	r.body = body
	return nil
}

type fakeBuildError struct{}

func (fakeBuildError) Error() string    { return "build failed\nmore" }
func (fakeBuildError) BuildLog() string { return "tail" }

func TestBuildRecordsFailureAndLog(t *testing.T) {
	host := &fakeHost{err: fakeBuildError{}}
	recorder := &fakeRecorder{}
	b := &Builder{Recorder: recorder, Now: func() time.Time { return time.Unix(1, 0) }}
	_, err := b.Build(context.Background(), host, "room", "host", "owner/repo", "toolchain", "base", "tree", nil)
	body, ok := recorder.body.(contract.ImageFailed)
	if err == nil || !ok || body.Reason != "build failed" || body.LogTail != "tail" {
		t.Fatalf("body %+v, error %v", recorder.body, err)
	}
}

func TestBuildRecordsSuccess(t *testing.T) {
	host := &fakeHost{}
	recorder := &fakeRecorder{}
	b := &Builder{Recorder: recorder, Now: func() time.Time { return time.Unix(1, 0) }}
	tag, err := b.Build(context.Background(), host, "room", "host", "owner/repo", "toolchain", "base", "tree", nil)
	body, ok := recorder.body.(contract.ImageBuilt)
	if err != nil || !ok || body.Tag != tag || host.calls != 1 {
		t.Fatalf("body %+v, error %v", recorder.body, err)
	}
}
