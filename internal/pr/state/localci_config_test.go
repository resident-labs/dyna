package state

import (
	"context"
	"testing"
)

// A view words a local CI configuration state itself. A value that no client
// knows, such as a Git or YAML error, must never reach it as prose.
func TestUnknownLocalCIConfigStateIsDroppedOnRead(t *testing.T) {
	root := newRoot(t)
	ctx := context.Background()
	if err := Update(ctx, root, func(st *State) error {
		st.Records["owner/repo#1"] = &Record{Key: "owner/repo#1", State: "open"}
		st.Records["owner/repo#2"] = &Record{Key: "owner/repo#2", State: "open", LocalCIConfig: LocalCIConfigHeadMissing}
		st.Records["owner/repo#3"] = &Record{Key: "owner/repo#3", State: "open", LocalCIConfig: LocalCIConfigState("fatal: bad object HEAD")}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Records["owner/repo#3"].LocalCIConfig; got != LocalCIConfigNone {
		t.Fatalf("unknown local CI state reached a view: %q", got)
	}
	if got := st.Records["owner/repo#2"].LocalCIConfig; got != LocalCIConfigHeadMissing {
		t.Fatalf("known local CI state was lost: %q", got)
	}
	if got := st.Records["owner/repo#1"].LocalCIConfig; got != LocalCIConfigNone {
		t.Fatalf("unset local CI state changed: %q", got)
	}
}
