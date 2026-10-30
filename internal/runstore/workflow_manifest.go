package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

var ErrWorkflowManifestIncomplete = errors.New("workflow manifest is incomplete or unsafe")

type WorkflowCallKind string

const (
	WorkflowCallCommand WorkflowCallKind = "command"
	WorkflowCallAgent   WorkflowCallKind = "agent"
	WorkflowCallPost    WorkflowCallKind = "post"
)

type WorkflowManifestCall struct {
	Kind       WorkflowCallKind
	Identity   string
	Ordinal    int
	ExternalID string
	Result     json.RawMessage
	Error      string
}
type WorkflowManifest struct {
	Complete     bool
	UnsafeReason string
	Calls        []WorkflowManifestCall
}

func BeginWorkflowManifest(ctx context.Context, runID string) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, `INSERT INTO workflow_manifests(run_id) VALUES(?)`, runID)
		return err
	})
}

func RegisterWorkflowCall(ctx context.Context, runID string, kind WorkflowCallKind, identity string, ordinal int) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(c *sql.Conn) error {
		r, err := c.ExecContext(ctx, `INSERT INTO workflow_manifest_calls(run_id,kind,identity,ordinal) SELECT ?,?,?,? FROM workflow_manifests WHERE run_id=? AND complete=0`, runID, kind, identity, ordinal, runID)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("workflow manifest is not recording")
		}
		return nil
	})
}

func CompleteWorkflowCall(ctx context.Context, runID string, kind WorkflowCallKind, identity string, ordinal int, externalID string, result any, callErr error) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var errorText string
	if callErr != nil {
		errorText = callErr.Error()
	}
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(c *sql.Conn) error {
		r, err := c.ExecContext(ctx, `UPDATE workflow_manifest_calls SET result=?,error=?,external_id=? WHERE run_id=? AND kind=? AND identity=? AND ordinal=? AND result IS NULL`, string(payload), errorText, externalID, runID, kind, identity, ordinal)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("workflow call has no unfinished intent")
		}
		return nil
	})
}

func MarkWorkflowManifestUnsafe(ctx context.Context, runID, reason string) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(c *sql.Conn) error {
		r, err := c.ExecContext(ctx, `UPDATE workflow_manifests SET unsafe_reason=CASE WHEN unsafe_reason='' THEN ? ELSE unsafe_reason END WHERE run_id=? AND complete=0`, reason, runID)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("workflow manifest is not recording")
		}
		return nil
	})
}

func SealWorkflowManifest(ctx context.Context, runID string) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(c *sql.Conn) error {
		r, err := c.ExecContext(ctx, `UPDATE workflow_manifests SET complete=1 WHERE run_id=? AND complete=0 AND unsafe_reason='' AND NOT EXISTS (SELECT 1 FROM workflow_manifest_calls WHERE run_id=? AND result IS NULL)`, runID, runID)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrWorkflowManifestIncomplete
		}
		return nil
	})
}

func ReadWorkflowManifest(ctx context.Context, runID string) (WorkflowManifest, error) {
	db, err := runsDB()
	if err != nil {
		return WorkflowManifest{}, err
	}
	var out WorkflowManifest
	if err := db.QueryRowContext(ctx, `SELECT complete,unsafe_reason FROM workflow_manifests WHERE run_id=?`, runID).Scan(&out.Complete, &out.UnsafeReason); err != nil {
		return out, err
	}
	rows, err := db.QueryContext(ctx, `SELECT kind,identity,ordinal,external_id,result,error FROM workflow_manifest_calls WHERE run_id=? ORDER BY kind,identity,ordinal`, runID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var call WorkflowManifestCall
		var result sql.NullString
		if err := rows.Scan(&call.Kind, &call.Identity, &call.Ordinal, &call.ExternalID, &result, &call.Error); err != nil {
			return out, err
		}
		if result.Valid {
			call.Result = json.RawMessage(result.String)
		}
		out.Calls = append(out.Calls, call)
	}
	return out, rows.Err()
}
