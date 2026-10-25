package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/account/store"
)

// One saved row of a harness this release does not run must not hide the other
// accounts: the list returns the good row and names the bad one.
func TestListSkipsUnknownHarnessRowAndNamesIt(t *testing.T) {
	a := testAccounts(t)
	good, err := a.Create("codex", "Good")
	if err != nil {
		t.Fatal(err)
	}
	futureID := strings.Repeat("a", 32)
	err = (&store.Store{Root: a.Root}).Update(func(state *store.State) error {
		state.Entries = append(state.Entries, store.Entry{Metadata: store.Metadata{ID: futureID, Harness: "harness-from-the-future", Label: "Old row", NativeHome: filepath.Join(a.Root, "harness-from-the-future", futureID)}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, problems, err := a.ListWithProblems()
	if err != nil {
		t.Fatalf("ListWithProblems = %v, want the good row despite the bad one", err)
	}
	if len(rows) != 1 || rows[0].ID != good.ID {
		t.Fatalf("rows = %+v, want only %s", rows, good.ID)
	}
	if len(problems) != 1 || problems[0].ID != futureID || problems[0].Reason != AccountUnknownHarness || problems[0].Label != "Old row" {
		t.Fatalf("problems = %+v, want the unknown-harness row %s", problems, futureID)
	}

	// Callers that read only accounts get the same rows and no error.
	plain, err := a.List()
	if err != nil || len(plain) != 1 || plain[0].ID != good.ID {
		t.Fatalf("List = %+v, %v", plain, err)
	}
}
