package runstore

import (
	"errors"
	"os"
	"testing"
)

func TestRemoveWithChecksBeforeHook(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_removal-hook")
	run, err := Create("remove", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	hook := func(Meta) error { called = true; return nil }
	if err := RemoveWith(run.Meta.ID, hook); err == nil || called {
		t.Fatalf("running removal = %v, hook called = %v", err, called)
	}
	run.Finish("ok", "", nil)
	hookErr := errors.New("retain storage")
	if err := RemoveWith(run.Meta.ID, func(Meta) error { return hookErr }); !errors.Is(err, hookErr) {
		t.Fatalf("hook failure = %v", err)
	}
	if _, err := ReadMeta(run.Meta.ID); err != nil {
		t.Fatalf("hook failure removed metadata: %v", err)
	}
	if err := RemoveWith(run.Meta.ID, hook); err != nil || !called {
		t.Fatalf("finished removal = %v, hook called = %v", err, called)
	}
	if _, err := os.Stat(run.Dir); !os.IsNotExist(err) {
		t.Fatalf("removed directory: %v", err)
	}
}
