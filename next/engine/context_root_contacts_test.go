package engine

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestContextRootContacts(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	var names, fullNames []string
	profile := strings.Repeat("p", 40)
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		for i := range 25 {
			room := fmt.Sprintf("room-%02d-%s", i, strings.Repeat("r", 56))
			if _, err := tx.ExecContext(ctx, `INSERT INTO rooms(id,name,created_at,cpus,mem_bytes,state) VALUES(?,?,?,1,1,?)`, room, room, i, "open"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES(?,?,'root',?,'open','secret goal',?)`, room, room, profile, i); err != nil {
				return err
			}
			if i < 23 {
				fullNames = append(fullNames, fmt.Sprintf("%s:root (%s)", room, profile))
			}
			if i < 20 {
				names = append(names, fmt.Sprintf("%s:root (%s)", room, profile))
			}
			if i == 23 {
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET closed_at=1 WHERE id=?`, room); err != nil {
					return err
				}
			}
			if i == 24 {
				if _, err := tx.ExecContext(ctx, `UPDATE rooms SET state='deleting' WHERE id=?`, room); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	names = append(names, "and 3 more (context show dyna_contacts)")
	want := "siblings: " + strings.Join(names, ", ")
	for _, section := range []contract.ContextSectionKey{"", contract.ContextContacts} {
		res, problem := r.s.ContextSnapshot(ctx, r.root, contract.ContextSnapshotRequest{Section: section})
		if problem != nil {
			t.Fatal(problem)
		}
		var got string
		for _, sec := range res.Sections {
			if sec.Key == contract.ContextContacts {
				got = sec.Text
			}
		}
		if section == contract.ContextContacts {
			want = "siblings: " + strings.Join(fullNames, ", ")
		}
		if got != want {
			t.Fatalf("root contacts = %q, want %q", got, want)
		}
	}
	// Other rooms' contacts are only exposed to roots, not their workers.
	res, problem := r.s.ContextSnapshot(ctx, r.worker, contract.ContextSnapshotRequest{Section: contract.ContextContacts})
	if problem != nil || len(res.Sections) != 1 || res.Sections[0].Text != "parent: root (test)" {
		t.Fatalf("worker contacts = %+v, %v", res, problem)
	}
}
