package workflow

import (
	"errors"
	"fmt"
	"testing"

	"dyna-agent/internal/database"
)

// newerSchemaStartError is the failure D-402 reports: a shell supervisor the
// old binary spawned answers with the newer-schema sentinel as text, because
// the error crossed a process boundary.
func newerSchemaStartError() error {
	return fmt.Errorf("start shell supervisor: Error: %s: shells.version is version 6, this dyna knows up to 5", database.ErrNewerSchema)
}

// TestRunStopsForUpdateOnNewerSchema fails first without the fix: the first
// command's failure only rejects that step, the script keeps going, and the
// next command fails the same way, step after step.
func TestRunStopsForUpdateOnNewerSchema(t *testing.T) {
	fake := newFakeCommands()
	fake.startErr = newerSchemaStartError()
	_, _, err := runCommandScript(t, `
await run('first')
await run('second')
return 'done'
`, Options{Commands: fake})
	if !StoppedForUpdate(err) {
		t.Fatalf("Execute() error = %v; want the stop-for-update outcome", err)
	}
	if fake.attempts != 1 {
		t.Fatalf("attempted %d commands; want the run to stop before the second", fake.attempts)
	}
}

// TestRunStopsForUpdateWhenBinaryReplaced fails first without the fix: a
// replaced binary keeps starting steps until one of them fails.
func TestRunStopsForUpdateWhenBinaryReplaced(t *testing.T) {
	replaced := executableReplaced
	executableReplaced = func() bool { return true }
	t.Cleanup(func() { executableReplaced = replaced })
	fake := newFakeCommands()
	_, _, err := runCommandScript(t, `await run('first'); return 'done'`, Options{Commands: fake})
	if !StoppedForUpdate(err) {
		t.Fatalf("Execute() error = %v; want the stop-for-update outcome", err)
	}
	if len(fake.specs) != 0 {
		t.Fatalf("started %d commands; want none on the replaced binary", len(fake.specs))
	}
}

// TestStoppedForUpdateWrapsCause keeps the reporting honest: the outcome
// names the update stop and still shows the failure that caused it.
func TestStoppedForUpdateWrapsCause(t *testing.T) {
	cause := newerSchemaStartError()
	wrapped := fmt.Errorf("%w: %v", ErrStoppedForUpdate, cause)
	if !StoppedForUpdate(wrapped) || !errors.Is(wrapped, ErrStoppedForUpdate) {
		t.Fatal("the wrapped outcome must match StoppedForUpdate")
	}
	if StoppedForUpdate(cause) {
		t.Fatal("an ordinary failure is not a stop for update")
	}
}
