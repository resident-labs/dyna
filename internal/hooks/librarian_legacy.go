package hooks

import (
	"context"
	"database/sql"
	"strings"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// TODO: legacy/shim remove later — drops the librarian-merged and librarian-batch hooks that earlier releases added; remove once no store predates the librarian-main-only hooks.
// RemoveLegacyLibrarianHooks removes the librarian-merged and librarian-batch hooks
// that run dyna library wake from every repository. The librarian needs only
// librarian-main now, and the engine keeps its own deferred wakes. A store
// without these hooks is left unchanged, so the engine may call it on each start.
func (s Service) RemoveLegacyLibrarianHooks(ctx context.Context) error {
	db, err := s.documentsDB()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT repo FROM hook_documents WHERE repo <> ?", globalKey)
		if err != nil {
			return err
		}
		keys := []string{}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, key := range keys {
			d, err := readDocument(ctx, conn, key)
			if err != nil {
				return err
			}
			kept := make([]Hook, 0, len(d.Hooks))
			for _, h := range d.Hooks {
				if !isLegacyLibrarianHook(h) {
					kept = append(kept, h)
				}
			}
			if len(kept) == len(d.Hooks) {
				continue
			}
			d.Hooks = kept
			if err := writeDocument(ctx, conn, key, d); err != nil {
				return err
			}
		}
		return nil
	})
}

// isLegacyLibrarianHook matches the two hooks that ran dyna library wake on a
// schedule or after a merge. A hook with the same name and another command stays.
func isLegacyLibrarianHook(h Hook) bool {
	return (h.Name == "librarian-merged" || h.Name == "librarian-batch") && strings.Contains(h.Command, " library wake --repo ")
}
