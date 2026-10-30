package engine

import (
	"context"
	"database/sql"
	"dyna-agent/internal/workflowsvc"
	"log"
	"sync"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/state"
)

var updateNoticeMu sync.Mutex

// reportUpdateNotice retries a durable, typed outcome until delivery succeeds.
func reportUpdateNotice(ctx context.Context, w *state.Writer, notices *notify.Service, id string) {
	updateNoticeMu.Lock()
	defer updateNoticeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var pending bool
	if err := w.Read(ctx, func(q state.Reader) error {
		return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_update_notices WHERE run=?)", id).Scan(&pending)
	}); err != nil {
		log.Printf("read workflow update notice %s: %v", id, err)
		return
	}
	if !pending {
		return
	}
	replayRun, reason, err := workflowsvc.TryAutomaticReplay(ctx, id)
	if err != nil {
		log.Printf("check workflow replay %s: %v", id, err)
		return
	}
	result, err := notices.CompleteRunWithReplay(ctx, id, reason, replayRun)
	if err != nil {
		log.Printf("deliver workflow update notice %s: %v", id, err)
		return
	}
	if result.Message.ID == "" {
		log.Printf("deliver workflow update notice %s: no post was recorded", id)
		return
	}
	if err := w.Write(ctx, hub.Event{Topic: "runs", Key: id}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM run_update_notices WHERE run=?", id)
		return err
	}); err != nil {
		log.Printf("finish workflow update notice %s: %v", id, err)
	}
}

func reportUpdateNotices(ctx context.Context, w *state.Writer, notices *notify.Service) error {
	var ids []string
	if err := w.Read(ctx, func(q state.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT run FROM run_update_notices ORDER BY run")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range ids {
		reportUpdateNotice(ctx, w, notices, id)
	}
	return nil
}
