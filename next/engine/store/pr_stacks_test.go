package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestPRStackIdentityAndAtomicity(t *testing.T) {
	for _, mode := range []string{"case variant", "collision same owner", "collision other owner", "closed owner", "closing owner", "missing layer", "already stacked"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
				t.Fatal(err)
			}
			_, err = s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,parent,name,profile,state,goal,created_at) VALUES('lead','room',NULL,'lead','p','ready','',0),('child','room','lead','child','p','ready','',0),('other','room',NULL,'other','p','ready','',0);
 INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('Acme/App',1,'child','',''),('Acme/App',2,'child','','')`)
			if err != nil {
				t.Fatal(err)
			}
			var mutation string
			switch mode {
			case "collision same owner":
				mutation = `INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('acme/app',2,'child','','')`
			case "collision other owner":
				mutation = `INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('acme/app',2,'other','','')`
			case "closed owner":
				mutation = `UPDATE sessions SET closed_at=1 WHERE id='child'`
			case "closing owner":
				mutation = `UPDATE sessions SET state='closing' WHERE id='child'`
			case "missing layer":
				mutation = `DELETE FROM pr_owners WHERE number=2`
			case "already stacked":
				mutation = `UPDATE pr_owners SET stack=99,position=1 WHERE number=2`
			}
			if mutation != "" {
				if _, err := s.writer.ExecContext(ctx, mutation); err != nil {
					t.Fatal(err)
				}
			}
			prs := []int{1, 2}
			check := s.CheckPRStack(ctx, "ACME/APP", prs, "lead", "room")
			record := s.RecordPRStack(ctx, "ACME/APP", prs, "lead", "room", 50)
			if mode == "case variant" {
				if check != nil || record != nil {
					t.Fatalf("%v %v", check, record)
				}
				p, err := s.PR(ctx, "acme/app", 2)
				if err != nil || p.Stack != 50 || p.Position != 2 || p.StackSize != 2 || p.Session != contract.SessionID("child") {
					t.Fatalf("%+v %v", p, err)
				}
				return
			}
			if check == nil || record == nil {
				t.Fatalf("accepted %s: %v %v", mode, check, record)
			}
			if !strings.Contains(record.Error(), "PR #") {
				t.Fatal(record)
			}
			var stackCount int
			if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM pr_owners WHERE stack=50`).Scan(&stackCount); err != nil || stackCount != 0 {
				t.Fatalf("partial write %d %v", stackCount, err)
			}
		})
	}
}
