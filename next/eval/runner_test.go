package eval

import (
	"context"
	"errors"
	"testing"
)

type blockedBackend struct{ calls int }

func (*blockedBackend) Preflight(context.Context) error { return errors.New("missing route") }
func (b *blockedBackend) Prepare(context.Context, Cell) (Fixture, error) {
	b.calls++
	return Fixture{}, nil
}
func (*blockedBackend) BeginAttempt(context.Context, Cell, int) error           { return nil }
func (*blockedBackend) FinishAttempt(context.Context, Cell, int, Attempt) error { return nil }
func (*blockedBackend) Execute(context.Context, Cell, Fixture) (Attempt, error) {
	return Attempt{}, nil
}
func (*blockedBackend) Cleanup(context.Context, Cell, Fixture) error { return nil }

type incompleteFixtureBackend struct{ blockedBackend }

func (*incompleteFixtureBackend) Preflight(context.Context) error { return nil }

func TestIncompleteFixtureStartsNoModel(t *testing.T) {
	backend := &incompleteFixtureBackend{}
	err := RunSuite(context.Background(), backend, func(CellResult) error { t.Fatal("unexpected result"); return nil })
	if err == nil || backend.calls != 1 {
		t.Fatalf("err=%v prepares=%d", err, backend.calls)
	}
}

func TestMissingPrerequisiteStartsNoTasks(t *testing.T) {
	backend := &blockedBackend{}
	err := RunSuite(context.Background(), backend, func(CellResult) error { t.Fatal("unexpected result"); return nil })
	if err == nil || backend.calls != 0 {
		t.Fatalf("err=%v prepares=%d", err, backend.calls)
	}
}
