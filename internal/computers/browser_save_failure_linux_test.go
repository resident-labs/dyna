//go:build linux

package computers

import (
	"context"
	"os"
	"testing"
)

func TestBrowserSaveFailurePreservesProfile(t *testing.T) {
	for _, operation := range []string{"stop", "destroy", "offline-destroy"} {
		t.Run(operation, func(t *testing.T) {
			browserTestEnvironment(t)
			runtime, err := os.MkdirTemp("", "bsf-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(runtime)
			t.Setenv("XDG_RUNTIME_DIR", runtime)
			ctx := context.Background()
			id := testSessionID(t, "save-failure")
			s, err := newComputer(ctx, id, "test", SupervisorOptions{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.cleanup(false)
			profile := computerBrowserProfile(id)
			browserTestFile(t, profile, "Default/Cookies", "unsaved login state")
			if err := os.WriteFile(browserLeasePath(id), []byte("{invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			var saveErr error
			if operation == "stop" {
				_, saveErr = s.dispatch(ctx, Request{Op: "stop"})
			} else if operation == "destroy" {
				d := &daemon{ctx: ctx, computers: map[string]*supervisor{id: s}}
				_, saveErr = d.dispatch(ctx, Request{Op: "destroy", Session: id})
			} else {
				saveErr = destroyOffline(context.Background(), id)
			}
			if saveErr == nil {
				t.Error("save-back failure was reported as success")
			}
			if got := browserTestRead(t, profile, "Default/Cookies"); got != "unsaved login state" {
				t.Fatalf("unsaved browser state changed: %q", got)
			}
		})
	}
}
