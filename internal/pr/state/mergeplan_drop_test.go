package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The retired merge planner saved mergePlans and mergePlanInputs in the
// index. The next write after an upgrade must drop them.
func TestWriteDropsRetiredMergePlans(t *testing.T) {
	root := newRoot(t)
	legacy := `{"version":1,"records":{},"mergePlans":{"room":[{"repo":"owner/repo","base":"main","entries":[]}]},"mergePlanInputs":{"room":"signature"}}`
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	st.Records["k"] = &Record{Key: "k", State: "open"}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := mustOpen(t, root).QueryRow("SELECT body FROM pr_state").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "mergePlan") {
		t.Fatalf("retired merge plans survived a write: %s", raw)
	}
}
