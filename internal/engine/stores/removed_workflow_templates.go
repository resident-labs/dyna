package stores

import (
	"context"
	"database/sql"

	"dyna-agent/internal/database"
)

func migrateRemovedWorkflowTemplates(dir string) error {
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "removed_workflow_templates.version", 1, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS workflow_template_seeds"); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), "DELETE FROM meta WHERE key='workflowtemplate.version'")
		return err
	})
}
