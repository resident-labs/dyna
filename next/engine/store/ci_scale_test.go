package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/box"
	"testing"
)

func TestCIScaleStoreMigrationAndPersistence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddHost(ctx, box.Host{Name: "h"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (box.Layout{Data: dir}).Store())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP INDEX deliveries_open; ALTER TABLE deliveries DROP COLUMN taken_at; ALTER TABLE turns DROP COLUMN model_sent_at; ALTER TABLE deliveries DROP COLUMN immediate",
		"DROP TABLE pr_stack_layers", "ALTER TABLE pr_owners DROP COLUMN stack", "ALTER TABLE pr_owners DROP COLUMN position", "ALTER TABLE hosts DROP COLUMN ci_scale", "DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; UPDATE meta SET value='97' WHERE key='schema_version'"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "h"); h.CIScale != 1 {
		t.Fatalf("migrated default: %+v", h)
	}
	n := 4
	if err = s.SetHostSwitches(ctx, "h", nil, nil, &n, false, hostSwitchEvent); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if h := hostSwitches(t, s, "h"); h.CIScale != 4 {
		t.Fatalf("reopened: %+v", h)
	}
	for _, bad := range []int{0, 5} {
		if err = s.SetHostSwitches(ctx, "h", nil, nil, &bad, false, hostSwitchEvent); err == nil {
			t.Fatalf("SQL constraint accepted %d", bad)
		}
	}
	if h := hostSwitches(t, s, "h"); h.CIScale != 4 {
		t.Fatalf("refused write changed capacity: %+v", h)
	}
}
