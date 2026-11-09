package engine

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestContextContactsSection(t *testing.T) {
	r := newGrantRig(t)
	for _, section := range []contract.ContextSectionKey{"", contract.ContextContacts} {
		for _, who := range []contract.Principal{r.root, r.worker} {
			res, problem := r.s.ContextSnapshot(t.Context(), who, contract.ContextSnapshotRequest{Section: section})
			if problem != nil {
				t.Fatal(problem)
			}
			var contacts []contract.ContextSection
			for _, sec := range res.Sections {
				if sec.Key == contract.ContextContacts {
					contacts = append(contacts, sec)
				}
			}
			if who == r.root {
				if len(contacts) != 0 {
					t.Fatalf("root has contacts: %+v", contacts)
				}
			} else if len(contacts) != 1 || contacts[0].Text != "parent: root (test)" {
				t.Fatalf("parent-only contacts: %+v", contacts)
			}
		}
	}
}

func TestContextContactsSiblingCap(t *testing.T) {
	for _, n := range []int{19, 20, 21, 23} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := newGrantRig(t)
			ctx := t.Context()
			var names []string
			// Use the longest allowed names and profiles: the section budget must still fit all 20 entries.
			profile := strings.Repeat("p", 40)
			if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
				for i := range n {
					name := fmt.Sprintf("%02d-%s", i, strings.Repeat("n", 61))
					if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES(?,?,?,?,'open','',?,?)`, fmt.Sprint("contact-", i), r.room, name, profile, i, r.root.Session); err != nil {
						return err
					}
					if i < 20 {
						names = append(names, fmt.Sprintf("%s (%s)", name, profile))
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if n > 20 {
				names = append(names, fmt.Sprintf("and %d more (session list)", n-20))
			}
			want := "parent: root (test)\nsiblings: " + strings.Join(names, ", ")
			for _, section := range []contract.ContextSectionKey{"", contract.ContextContacts} {
				res, problem := r.s.ContextSnapshot(ctx, r.worker, contract.ContextSnapshotRequest{Section: section})
				if problem != nil {
					t.Fatal(problem)
				}
				var got string
				for _, sec := range res.Sections {
					if sec.Key == contract.ContextContacts {
						got = sec.Text
					}
				}
				if got != want {
					t.Fatalf("contacts = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestContextContactsIgnoreSiblingActivity(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	sibling, problem := r.s.SessionStart(ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: "sibling", Profile: "test", Goal: "initial goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	snapshot := func() string {
		t.Helper()
		res, problem := r.s.ContextSnapshot(ctx, r.worker, contract.ContextSnapshotRequest{})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, section := range res.Sections {
			if section.Key == contract.ContextContacts {
				full, problem := r.s.ContextSnapshot(ctx, r.worker, contract.ContextSnapshotRequest{Section: contract.ContextContacts})
				if problem != nil || len(full.Sections) != 1 || full.Sections[0].Text != section.Text {
					t.Fatalf("explicit section differs: %+v %v", full, problem)
				}
				return section.Text
			}
		}
		t.Fatal("dyna_contacts is missing")
		return ""
	}
	before := snapshot()
	if want := "parent: root (test)\nsiblings: sibling (test)"; before != want {
		t.Fatalf("contacts = %q, want %q", before, want)
	}
	unchanged := func() {
		t.Helper()
		if got := snapshot(); got != before {
			t.Fatalf("sibling activity changed contacts: before %q, after %q", before, got)
		}
	}
	for _, state := range []contract.BoxState{contract.BoxReady, contract.BoxStopped} {
		if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE boxes SET state=? WHERE session=?", state, sibling.Session)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		unchanged()
	}
	who := contract.Principal{Kind: contract.PrincipalSession, Session: sibling.Session}
	goal, problem := r.s.GoalSet(ctx, who, contract.GoalSetRequest{Goal: "g2", Text: "new goal", DoneWhen: "finished"})
	if problem != nil {
		t.Fatal(problem)
	}
	unchanged()
	if _, problem := r.s.GoalItem(ctx, who, contract.GoalItemRequest{Goal: goal.Goal, Text: "new item"}); problem != nil {
		t.Fatal(problem)
	}
	unchanged()
	if _, problem := r.s.GoalSet(ctx, who, contract.GoalSetRequest{Goal: goal.Goal, Text: "changed goal"}); problem != nil {
		t.Fatal(problem)
	}
	unchanged()
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET shell_jobs=2 WHERE id=?", sibling.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	unchanged()
}
