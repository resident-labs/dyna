package sessionlife

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestSessionReadsWaitForEngineSchemaUpgrade(t *testing.T) {
	raw := `{"id":"a","instructions":{"text":"keep"},"instructionsKnown":{"text":"known"},"future":{"keep":true}}`
	sessions := v3Store(t, [2]string{"a", raw})
	listed := v3Store(t, [2]string{"a", raw})
	blocked := v3Store(t, [2]string{"a", raw})
	cancelled := v3Store(t, [2]string{"a", raw})
	failed := v3Store(t, [2]string{"a", raw})
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	oldRetry, oldPause := recordSchemaRetry, recordSchemaPause
	t.Cleanup(func() {
		recordSchemaRetry, recordSchemaPause = oldRetry, oldPause
	})
	// DisownSchema also simulates a failed engine migration. An ordinary
	// external reader still retries an older schema while the engine updates.
	recordSchemaRetry = func(err error) bool { return database.IsOlderSchema(err, "sessions.records") }
	waits := 0
	migrate := sessions
	recordSchemaPause = func(ctx context.Context) error {
		waits++
		if err := ctx.Err(); err != nil {
			return err
		}
		database.OwnSchema()
		err := Migrate(filepath.Dir(migrate))
		database.DisownSchema()
		return err
	}
	var got map[string]json.RawMessage
	if err := Load(sessions, "a", &got); err != nil {
		t.Fatal(err)
	}
	if waits != 1 || string(got["instructions"]) != `{"text":"keep"}` || string(got["instructionsKnown"]) != `{"text":"known"}` {
		t.Fatalf("Load after migration: waits = %d, record = %v", waits, got)
	}
	migrate = listed
	calls := 0
	if err := Each(listed, false, func(id string, stored []byte, err error) error {
		calls++
		var record map[string]json.RawMessage
		if err == nil {
			err = json.Unmarshal(stored, &record)
		}
		if id != "a" || err != nil || string(record["instructions"]) != `{"text":"keep"}` || string(record["instructionsKnown"]) != `{"text":"known"}` || string(record["future"]) != `{"keep":true}` {
			t.Fatalf("Each after migration: id = %q, record = %v, error = %v", id, record, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || waits != 2 {
		t.Fatalf("Each after migration: calls = %d, total waits = %d", calls, waits)
	}

	// An unavailable engine leaves the schema old. The reader returns the
	// schema error after the same 120 waits as the shell-schema contract.
	waits = 0
	recordSchemaPause = func(context.Context) error {
		waits++
		return nil
	}
	if err := Load(blocked, "a", &got); !database.IsOlderSchema(err, "sessions.records") || waits != 120 {
		t.Fatalf("unavailable engine: waits = %d, error = %v", waits, err)
	}

	// Cancellation stops the wait and keeps the schema error available.
	waits = 0
	recordSchemaPause = func(context.Context) error {
		waits++
		return context.Canceled
	}
	if err := Load(cancelled, "a", &got); !errors.Is(err, context.Canceled) || !database.IsOlderSchema(err, "sessions.records") || waits != 1 {
		t.Fatalf("cancelled read: waits = %d, error = %v", waits, err)
	}

	// An engine whose own migration failed must not be waited on.
	recordSchemaRetry = oldRetry
	waits = 0
	recordSchemaPause = func(context.Context) error {
		waits++
		return nil
	}
	if err := Load(failed, "a", &got); !database.IsOlderSchema(err, "sessions.records") || waits != 0 {
		t.Fatalf("failed engine migration: waits = %d, error = %v", waits, err)
	}
}
