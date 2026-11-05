package engine

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"testing"
	"time"
)

func TestInterruptUsesActualSenderAncestry(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err = st.CreateRoom(ctx, "r", "r", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('root','r','root','p','open','',0,NULL),('parent','r','build-lead','p','open','',1,'root'),('worker','r','worker','p','open','',2,'parent'),('sibling','r','sibling','p','open','',3,'parent'),('child','r','child','p','open','',4,'worker')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{store: st}
	for _, tc := range []struct {
		kind contract.PrincipalKind
		id   contract.SessionID
		want contract.InterruptKind
		name string
	}{{contract.PrincipalOwner, "", contract.InterruptOwner, ""}, {contract.PrincipalSession, "parent", contract.InterruptParent, "build-lead"}, {contract.PrincipalSession, "root", contract.InterruptAncestor, "root"}, {contract.PrincipalSession, "sibling", "", ""}, {contract.PrincipalSession, "child", "", ""}, {contract.PrincipalEngine, "", "", ""}} {
		t.Run(string(tc.kind)+string(tc.id), func(t *testing.T) {
			got, err := s.inputInterrupt(ctx, "worker", contract.Event{Room: "r", From: contract.Principal{Kind: tc.kind, Session: tc.id}}, contract.SurfaceFeed)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unexpected %+v", got)
				}
				return
			}
			if got == nil || got.Kind != tc.want || got.Name != tc.name {
				t.Fatalf("got %+v", got)
			}
		})
	}
	got, err := s.inputInterrupt(ctx, "worker", contract.Event{Room: "r", From: contract.Principal{Kind: contract.PrincipalOwner}}, contract.SurfaceEngine)
	if err != nil || got != nil {
		t.Fatalf("owner-authored engine notice interrupted: %+v %v", got, err)
	}
}
