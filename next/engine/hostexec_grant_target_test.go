package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// A holder hands its host down to a child it names by ID without a kind, or by name, as the other tools allow.
func TestHostGrantTargetIsNamedByIDOrName(t *testing.T) {
	for _, byName := range []bool{false, true} {
		r := newGrantRig(t)
		ctx := t.Context()
		if err := r.s.store.CreateGrant(ctx, contract.Grant{ID: "grant_root", Kind: contract.GrantHostExec, Room: r.room, To: r.root, Hosts: []contract.HostName{"host"}, By: r.owner, Created: r.s.now()}); err != nil {
			t.Fatal(err)
		}
		to := r.worker.Session
		if byName {
			worker, err := r.s.store.Session(ctx, r.worker.Session)
			if err != nil {
				t.Fatal(err)
			}
			to = contract.SessionID(worker.Name)
		}
		args := contract.HostexecGrantArgs{Room: r.room, To: contract.Principal{Session: to}, Hosts: []contract.HostName{"host"}, Reason: "run the benchmark"}
		res, p := r.s.OpSubmit(ctx, r.root, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}})
		if p != nil || res.Operation.State != contract.OpDone {
			t.Fatalf("to %q: %+v %v", to, res.Operation, p)
		}
		if r.grantsOfTest() != 1 {
			t.Fatalf("to %q: the child holds %d grants", to, r.grantsOfTest())
		}
	}
}

func TestHostGrantRetryFindsItsOperationAfterTheTargetClosed(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	if err := r.s.store.CreateGrant(ctx, contract.Grant{ID: "grant_root", Kind: contract.GrantHostExec, Room: r.room, To: r.root, Hosts: []contract.HostName{"host"}, By: r.owner, Created: r.s.now()}); err != nil {
		t.Fatal(err)
	}
	req := contract.OpSubmitRequest{ClientID: "once", Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: contract.HostexecGrantArgs{Room: r.room, To: contract.Principal{Session: r.worker.Session}, Hosts: []contract.HostName{"host"}, Reason: "run the benchmark"}}}
	first, p := r.s.OpSubmit(ctx, r.root, req)
	if p != nil {
		t.Fatal(p)
	}
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE sessions SET state='closed',closed_at=1 WHERE id=?", r.worker.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, p := r.s.resolveSession(ctx, r.root, r.worker.Session); p == nil {
		t.Fatal("the closed session still resolves")
	}
	again, p := r.s.OpSubmit(ctx, r.root, req)
	if p != nil || again.Operation.ID != first.Operation.ID {
		t.Fatalf("retry: %+v %v", again.Operation, p)
	}
}
