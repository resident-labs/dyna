package engine

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptReadOwnerAndTurn(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	var seq int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT seq FROM deliveries WHERE to_kind='session' AND to_id=? LIMIT 1", caller.Session).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:00Z\",\"message\":{\"role\":\"user\",\"deliveryId\":\"%d\"}}\n{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:01Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":\"c\",\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}]}}\n", seq)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	req := contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a"}
	if _, e := s.TranscriptRead(ctx, caller, req); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("session read: %v", e)
	}
	got, e := s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
	if e != nil || len(got.Steps) != 1 || got.Steps[0].Label != "read a.go" || got.Steps[0].Ended != nil {
		t.Fatalf("read = %+v %v", got, e)
	}
	req.Turn = "other"
	got, e = s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
	if e != nil || len(got.Steps) != 0 {
		t.Fatalf("filter = %+v %v", got, e)
	}
}
