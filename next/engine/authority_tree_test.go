package engine

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
	"os"
	"testing"
)

func TestAuthorityTree(t *testing.T) {
	for _, coverage := range []string{"none", "partial", "all", "owner"} {
		t.Run(coverage, func(t *testing.T) {
			r := newGrantRig(t)
			ctx := t.Context()
			child, problem := r.s.SessionStart(ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: "fork-review", Profile: "test", Parent: r.worker.Session})
			if problem != nil {
				t.Fatal(problem)
			}
			target := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
			if coverage == "all" || coverage == "partial" {
				hosts := []contract.HostName{"a"}
				if coverage == "all" {
					hosts = append(hosts, "b")
				}
				if err := r.s.store.CreateGrant(ctx, contract.Grant{ID: "grant_lead", Kind: contract.GrantHostExec, Room: r.room, To: r.worker, Hosts: hosts, By: r.owner, Created: r.s.now()}); err != nil {
					t.Fatal(err)
				}
			}
			if coverage == "owner" {
				if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE grants SET revoked_at=1 WHERE to_id=?", r.root.Session)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			args := contract.HostexecGrantArgs{Room: r.room, To: target, Hosts: []contract.HostName{"a", "b"}, Reason: "merge the reviewed PR with gh"}
			res, problem := r.s.OpSubmit(ctx, target, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}})
			if problem != nil {
				t.Fatal(problem)
			}
			peer, p := r.s.SessionStart(ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: "peer", Profile: "test"})
			if p != nil {
				t.Fatal(p)
			}
			peerWho := contract.Principal{Kind: contract.PrincipalSession, Session: peer.Session}
			if err := r.s.store.CreateGrant(ctx, contract.Grant{ID: "grant_peer", Kind: contract.GrantHostExec, Room: r.room, To: peerWho, Hosts: []contract.HostName{"*"}, By: r.owner, Created: r.s.now()}); err != nil {
				t.Fatal(err)
			}
			if _, p := r.s.OpDecide(ctx, peerWho, contract.OpDecideRequest{ID: res.Operation.ID, Approve: true}); p == nil {
				t.Fatal("unrelated holder decided")
			}
			listed, p := r.s.RoomList(ctx, r.owner, contract.RoomListRequest{})
			if p != nil {
				t.Fatal(p)
			}
			for _, room := range listed.Rooms {
				if room.ID == r.room && len(room.Waiting) != map[bool]int{true: 1, false: 0}[coverage == "owner"] {
					t.Fatalf("owner attention %+v", room.Waiting)
				}
			}
			want := r.root.Session
			if coverage == "all" {
				want = r.worker.Session
			}
			if coverage == "owner" {
				want = ""
			}
			if res.Operation.Decider != want {
				t.Fatalf("decider %s, want %s", res.Operation.Decider, want)
			}
			var deliveries int
			if err := r.s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='op.requested'").Scan(&deliveries); err != nil {
				t.Fatal(err)
			}
			if want != "" {
				var to, surface, reply string
				var wake bool
				if err := r.s.store.Read().QueryRowContext(ctx, "SELECT d.to_id,d.surface,d.reply_kind,d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='op.requested'").Scan(&to, &surface, &reply, &wake); err != nil {
					t.Fatal(err)
				}
				if to != string(want) || surface != "session" || reply != "none" || !wake {
					t.Fatalf("delivery %s %s %s %v", to, surface, reply, wake)
				}
			}
			expected := 1
			if want == "" {
				expected = 0
			}
			if deliveries != expected {
				t.Fatalf("deliveries %d", deliveries)
			}
			if coverage != "all" {
				if _, p := r.s.OpDecide(ctx, r.worker, contract.OpDecideRequest{ID: res.Operation.ID, Approve: true}); p == nil {
					t.Fatal("nonholder decided")
				}
			}
			decider := r.owner
			if want != "" {
				decider = contract.Principal{Kind: contract.PrincipalSession, Session: want}
			}
			if _, p := r.s.OpDecide(ctx, target, contract.OpDecideRequest{ID: res.Operation.ID, Approve: true}); p == nil {
				t.Fatal("self decided")
			}
			if _, p := r.s.OpDecide(ctx, decider, contract.OpDecideRequest{ID: res.Operation.ID, Approve: true}); p != nil {
				t.Fatal(p)
			}
			waitForOperation(t, r.s, ctx, res.Operation.ID, contract.OpDone)
			for _, who := range []contract.Principal{target, r.worker, r.root} {
				holds, err := r.s.HostexecGrant().Holds(ctx, args, who.Session)
				if err != nil || !holds {
					t.Fatalf("path %s: %v %v", who.Session, holds, err)
				}
			}
			if coverage == "all" {
				grants, err := r.s.store.ListActiveGrants(ctx, r.room, r.worker, r.s.now())
				if err != nil || len(grants) != 1 {
					t.Fatalf("duplicate lead grants: %+v %v", grants, err)
				}
			}
			// A holder passes access down immediately and cannot target an ancestor.
			args.Hosts = []contract.HostName{"a"}
			immediate, p := r.s.OpSubmit(ctx, r.worker, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}})
			if p != nil || immediate.Operation.State != contract.OpDone || immediate.Operation.Decision.By != r.worker {
				t.Fatalf("handdown: %+v %v", immediate, p)
			}
			args.To = r.root
			if _, p := r.s.OpSubmit(ctx, r.worker, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}}); p == nil {
				t.Fatal("handed upwards")
			}
		})
	}
}

func TestApprovalInputGolden(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	op := r.ask()
	event := contract.Event{From: r.worker, Body: contract.OpRequested{ID: "op_example", By: r.worker, Card: op.Card}}
	frame, _, err := r.s.inputFrame(ctx, r.root.Session, event, contract.Delivery{Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := room.RenderDelivery(event.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := frame + "\n" + body + "\n"
	path := "testdata/approval-input.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("input differs:\n%s", got)
	}
}

func TestHolderAboveTargetButNotRequesterCannotDecide(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	child, p := r.s.SessionStart(ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: "target", Profile: "test", Parent: r.worker.Session})
	if p != nil {
		t.Fatal(p)
	}
	if err := r.s.store.CreateGrant(ctx, contract.Grant{ID: "grant_lead", Kind: contract.GrantHostExec, Room: r.room, To: r.worker, Hosts: []contract.HostName{"host"}, By: r.owner, Created: r.s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE grants SET revoked_at=1 WHERE to_id=?", r.root.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	args := contract.HostexecGrantArgs{Room: r.room, To: contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, Hosts: []contract.HostName{"host"}, Reason: "inspect"}
	res, p := r.s.OpSubmit(ctx, r.root, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}})
	if p != nil {
		t.Fatal(p)
	}
	if _, p := r.s.OpDecide(ctx, r.worker, contract.OpDecideRequest{ID: res.Operation.ID, Approve: true}); p == nil {
		t.Fatal("holder below requester decided")
	}
	grandchild, p := r.s.SessionStart(ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: "grandchild", Profile: "test", Parent: child.Session})
	if p != nil {
		t.Fatal(p)
	}
	args.To.Session = grandchild.Session
	handed, p := r.s.OpSubmit(ctx, r.worker, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}})
	if p != nil || handed.Operation.State != contract.OpDone {
		t.Fatalf("two-level handdown: %+v %v", handed, p)
	}
	for _, session := range []contract.SessionID{child.Session, grandchild.Session} {
		holds, err := r.s.holdsHostexecGrant(ctx, args, session)
		if err != nil || !holds {
			t.Fatalf("path %s: %v %v", session, holds, err)
		}
	}
}

func TestPendingListRefreshesAClosedDecider(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "owner"}[owner], func(t *testing.T) {
			r := newGrantRig(t)
			ctx := t.Context()
			child, p := r.s.SessionStart(ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: "target", Profile: "test", Parent: r.worker.Session})
			if p != nil {
				t.Fatal(p)
			}
			if err := r.s.store.CreateGrant(ctx, contract.Grant{ID: "grant_lead", Kind: contract.GrantHostExec, Room: r.room, To: r.worker, Hosts: []contract.HostName{"host"}, By: r.owner, Created: r.s.now()}); err != nil {
				t.Fatal(err)
			}
			asker := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
			args := contract.HostexecGrantArgs{Room: r.room, To: asker, Hosts: []contract.HostName{"host"}, Reason: "inspect"}
			res, p := r.s.OpSubmit(ctx, asker, contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "hostexec.grant", Args: args}})
			if p != nil || res.Operation.Decider != r.worker.Session {
				t.Fatalf("submit %+v %v", res, p)
			}
			// Move the requester out before its former parent's close, so its request remains pending.
			if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
				if _, err := tx.Exec("UPDATE sessions SET parent=? WHERE id=?", r.root.Session, child.Session); err != nil {
					return err
				}
				if _, err := tx.Exec("UPDATE sessions SET closed_at=? WHERE id=?", r.s.now().UnixMilli(), r.worker.Session); err != nil {
					return err
				}
				if owner {
					_, err := tx.Exec("UPDATE grants SET revoked_at=1 WHERE to_id=?", r.root.Session)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			list, p := r.s.OpList(ctx, r.owner, contract.OpListRequest{Room: r.room})
			if p != nil || len(list.Operations) != 1 {
				t.Fatalf("list %+v %v", list, p)
			}
			want := r.root.Session
			if owner {
				want = ""
			}
			if list.Operations[0].Decider != want {
				t.Fatalf("decider %s want %s", list.Operations[0].Decider, want)
			}
			events, err := r.s.store.Events(ctx, r.room, 0, 100, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if body, ok := event.Body.(contract.OpRequested); ok && body.ID == res.Operation.ID && body.Decider != r.worker.Session {
					t.Fatalf("history changed: %+v", body)
				}
			}
		})
	}
}
