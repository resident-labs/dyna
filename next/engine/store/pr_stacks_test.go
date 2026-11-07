package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestPRStackIdentityAndAtomicity(t *testing.T) {
	for _, mode := range []string{"case variant", "collision same owner", "collision other owner", "closed owner", "closing owner", "missing layer", "already stacked", "write failure"} {
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
			case "write failure":
				mutation = `CREATE TRIGGER fail_second_stack BEFORE UPDATE OF stack ON pr_owners WHEN NEW.number=2 BEGIN SELECT RAISE(ABORT,'layer write failed'); END`
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
			if (check == nil && mode != "write failure") || record == nil {
				t.Fatalf("accepted %s: %v %v", mode, check, record)
			}
			if mode != "write failure" && !strings.Contains(record.Error(), "PR #") {
				t.Fatal(record)
			}
			var memberCount int
			if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM pr_stack_layers").Scan(&memberCount); err != nil || memberCount != 0 {
				t.Fatalf("partial membership %d %v", memberCount, err)
			}
			var stackCount int
			if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM pr_owners WHERE stack=50`).Scan(&stackCount); err != nil || stackCount != 0 {
				t.Fatalf("partial write %d %v", stackCount, err)
			}
		})
	}
}

func TestPRStackMembershipSurvivesUntrack(t *testing.T) {
	for _, removed := range []int{1, 2} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
				t.Fatal(err)
			}
			_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('owner','room','owner','p','ready','',0)")
			if err != nil {
				t.Fatal(err)
			}
			track := func(n int) {
				t.Helper()
				if err := s.TrackPR(ctx, TrackedPR{PrSummary: contract.PrSummary{Repo: "Acme/App", Number: n, Session: "owner", State: contract.PrOpen}}); err != nil {
					t.Fatal(err)
				}
			}
			for _, n := range []int{1, 2} {
				track(n)
			}
			if err := s.RecordPRStack(ctx, "acme/app", []int{1, 2}, "owner", "room", 50); err != nil {
				t.Fatal(err)
			}
			if err := s.UntrackPR(ctx, "Acme/App", removed); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			remaining := 3 - removed
			p, err := s.PR(ctx, "ACME/APP", remaining)
			if err != nil || p.Stack != 50 || p.Position != remaining || p.StackSize != 2 {
				t.Fatalf("remaining %+v %v", p, err)
			}
			if err := s.TrackPR(ctx, TrackedPR{PrSummary: contract.PrSummary{Repo: "acme/app", Number: removed, Session: "owner", State: contract.PrOpen}}); err != nil {
				t.Fatal(err)
			}
			var storedStack, storedPosition int
			if err := s.Read().QueryRowContext(ctx, "SELECT stack,position FROM pr_owners WHERE number=?", removed).Scan(&storedStack, &storedPosition); err != nil || storedStack != 50 || storedPosition != removed {
				t.Fatalf("restored columns %d %d %v", storedStack, storedPosition, err)
			}
			for _, n := range []int{1, 2} {
				p, err := s.PR(ctx, "acme/app", n)
				if err != nil || p.Stack != 50 || p.Position != n || p.StackSize != 2 {
					t.Fatalf("retracked %+v %v", p, err)
				}
			}
			if err := s.CheckPRStack(ctx, "ACME/APP", []int{1, 2}, "owner", "room"); err == nil {
				t.Fatal("retracked stack accepted for restacking")
			}
		})
	}
}
