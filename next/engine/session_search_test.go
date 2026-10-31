package engine

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestSessionSearch(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET closed_at=1 WHERE id=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reworker", "auto-worker", "worker-new"} {
		if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Goal: "goal", Brief: "brief"}); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct {
		query        string
		limit, total int
		names        []string
	}{
		{"work", 2, 3, []string{"worker-new", "auto-worker"}},
		{"auto work", 0, 1, []string{"auto-worker"}},
		{"worker", 0, 3, []string{"worker-new", "auto-worker", "worker"}},
		{"missing", 0, 0, nil},
	} {
		r, e := s.SessionSearch(ctx, owner, contract.SessionSearchRequest{Query: tc.query, Limit: tc.limit})
		if e != nil || r.Total != tc.total || len(r.Sessions) != len(tc.names) {
			t.Fatalf("%q: %+v %v", tc.query, r, e)
		}
		for i, n := range tc.names {
			if r.Sessions[i].Name != n || r.Sessions[i].Closed != (n == "worker") {
				t.Fatalf("%q: %+v", tc.query, r)
			}
		}
	}
	for _, req := range []contract.SessionSearchRequest{{Query: " "}, {Query: "a b c d e f g h i"}, {Query: "ok", Limit: 51}} {
		if _, e := s.SessionSearch(ctx, owner, req); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("%+v: %v", req, e)
		}
	}
	if _, e := s.SessionSearch(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.SessionSearchRequest{Query: "work"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", e)
	}
}
