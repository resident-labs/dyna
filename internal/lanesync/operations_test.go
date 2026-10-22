package lanesync

import (
	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncStatusIncludesIntegrationAndSavedProgress(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	git(t, repo, "commit", "--allow-empty", "-m", "base")
	base := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	common := strings.TrimSpace(git(t, repo, "rev-parse", "--absolute-git-dir"))
	tree := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD^{tree}"))
	head := base
	for i := 0; i < 55; i++ {
		head = strings.TrimSpace(git(t, repo, "commit-tree", tree, "-p", head, "-m", fmt.Sprintf("local %d", i)))
	}
	git(t, repo, "update-ref", "refs/heads/dyna/integration", head)
	svc := Service{Dir: t.TempDir()}
	raw := map[string]any{"repositories": map[string]any{repo: map[string]any{"origin": map[string]any{"ahead": 55}}}, "exports": map[string]any{common: map[string]any{"commit": base}}, "operations": map[string]any{common: map[string]any{"kind": "refresh", "state": "testing", "head": head}}}
	if err := store.WriteJSON(filepath.Join(svc.Dir, "sync.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(svc.Dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(svc.Dir) })
	status, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]struct {
		Integration struct {
			Head       string `json:"head"`
			LastExport string `json:"lastExport"`
			Commits    []struct {
				SHA    string `json:"sha"`
				Title  string `json:"title"`
				Author string `json:"author"`
				At     string `json:"at"`
			} `json:"commits"`
		} `json:"integration"`
		Operation struct {
			State string `json:"state"`
		} `json:"operation"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	row := got[repo]
	if row.Integration.Head != head || row.Integration.LastExport != base || len(row.Integration.Commits) != 50 || row.Operation.State != "testing" {
		t.Fatalf("status=%s", data)
	}
	first := row.Integration.Commits[0]
	if first.SHA != head || first.Title != "local 54" || first.Author != "Test" || first.At == "" {
		t.Fatalf("commit=%+v", first)
	}
}
