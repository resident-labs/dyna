package stores

import (
	"dyna-agent/internal/database"
	"testing"
)

func TestRemoveWorkflowTemplateState(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec("CREATE TABLE workflow_template_seeds (registry TEXT); INSERT INTO meta(key,value) VALUES('workflowtemplate.version','2')"); err != nil {
		t.Fatal(err)
	}
	if err := migrateRemovedWorkflowTemplates(dir); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT (SELECT count(*) FROM sqlite_master WHERE name='workflow_template_seeds') + (SELECT count(*) FROM meta WHERE key='workflowtemplate.version')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("old template state remains: %d, %v", count, err)
	}
}
