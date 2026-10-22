package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/pr/state"
)

func TestMergeReadyDoesNotCopyUnrelatedRecords(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	key := "github.com/owner/repo#1"
	if err := e.s.Store.update(func(st *State) error {
		for i := range 20 {
			id := fmt.Sprintf("history-%d", i)
			st.Records[id] = &Record{Key: id, State: "closed", RoomID: e.author.RoomID}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expected, err := e.s.Store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	copies := 0
	read := readStore
	readStore = func(root string) (State, error) {
		st, err := read(root)
		// The real whole-state reader has already cloned these rows. Count
		// unrelated rows, rather than mocking or bypassing that reader.
		for id := range st.Records {
			if id != key {
				copies++
			}
		}
		return st, err
	}
	t.Cleanup(func() { readStore = read })
	current, ready, err := e.s.mergeReady(t.Context(), expected)
	if err != nil || !ready || current.Head != expected.Head {
		t.Fatalf("fresh ready PR: ready=%v head=%s error=%v", ready, current.Head, err)
	}
	if copies != 0 {
		t.Fatalf("merge readiness copied %d unrelated records; want 0", copies)
	}
}

func TestMergeReadyKeepsChangesCommittedDuringNetworkReads(t *testing.T) {
	e, fixtures, merged := mergeEnv(t, 1)
	key := "github.com/owner/repo#1"
	baseline, err := state.ReadSnapshot(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	run := e.s.GH.run
	for _, change := range []string{"head", "watch", "closed", "draft", "review", "policy"} {
		t.Run(change, func(t *testing.T) {
			// Keep the real reader, writer and HTTP fixture. Reset only PR
			// state, instead of opening six independent room/session stores.
			if err := e.s.Store.update(func(st *State) error { *st = baseline.Copy(); return nil }); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.s.GH.run = run })
			if change == "policy" {
				fixtures[1].Checks = append(fixtures[1].Checks, Check{Name: "new-required-check", Status: "completed", Conclusion: "failure"})
			}
			expected, ok := baseline.Record(key)
			if !ok {
				t.Fatal("missing baseline PR")
			}
			changed := false
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if err := run(ctx, cmd); err != nil {
					return err
				}
				// Commit after the full pull was read, before the caller gets
				// its response. No timing assumption or extra worker is needed.
				if !changed && strings.Contains(strings.Join(cmd.Args, " "), "/issues/1/comments?") {
					changed = true
					return e.s.Store.updateRecords([]string{key}, func(st *State) error {
						r := st.Records[key]
						switch change {
						case "head":
							r.Head = strings.Repeat("f", 40)
						case "watch":
							r.Watching = false
						case "closed":
							r.State = "closed"
						case "draft":
							r.IsDraft = true
						case "review":
							r.Verdict = "changes"
						case "policy":
							r.PolicyOverride.RequiredChecks = nil
							p := RoomPolicy(*st, r.RoomID)
							p.RequiredChecks = []string{"new-required-check"}
							if st.Policies == nil {
								st.Policies = map[string]ChangePolicy{}
							}
							st.Policies[r.RoomID] = p
						}
						return nil
					})
				}
				return nil
			}
			_, ready, err := e.s.mergeReady(t.Context(), expected)
			if err != nil || ready || !changed || len(*merged) != 0 {
				t.Fatalf("%s during network read: changed=%v ready=%v merged=%v error=%v", change, changed, ready, *merged, err)
			}
		})
	}
}

func TestMergeReadKeepsMissingAndAliasContracts(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	key := "github.com/owner/repo#1"
	if _, ready, err := e.s.mergeReady(t.Context(), Record{Key: "gone", Head: "old"}); err != nil || ready {
		t.Fatalf("removed candidate: ready=%v error=%v", ready, err)
	}
	if err := e.s.ConfirmHeads(t.Context(), "gone"); err == nil || !strings.Contains(err.Error(), "is not tracked") {
		t.Fatalf("missing explicit confirmation: %v", err)
	}
	if err := e.s.Store.updateRecords([]string{key}, func(st *State) error {
		st.Records[key].ChangeID = "change-alias"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ConfirmHeads(t.Context(), "change-alias", key); err != nil {
		t.Fatalf("alias confirmation: %v", err)
	}
}
