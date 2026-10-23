package goalkeeper

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

func TestUnknownNoticeIsNotAcknowledged(t *testing.T) {
	s := &Service{}
	done, err := s.deliverNotice(context.Background(), room.Record{}, &goals.RoomState{}, goals.Notice{Kind: "future_kind"})
	if done || err == nil || !strings.Contains(err.Error(), "future_kind") {
		t.Fatalf("unknown notice: done=%v, err=%v; want pending and error naming future_kind", done, err)
	}
}
