//go:build linux

package computers

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
)

func TestDaemonReportsItsBuild(t *testing.T) {
	original := callspec.Build
	t.Cleanup(func() { callspec.Build = original })
	callspec.Build = "dev+test"
	got, err := (&daemon{}).dispatch(context.Background(), Request{Op: "version"})
	if err != nil || got != "dev+test" {
		t.Fatalf("version = %v, %v", got, err)
	}
}

// D-419: a client newer than the daemon read "unknown computer operation" as a
// bug. The daemon names the service restart that ends the version skew.
func TestUnknownOperationNamesNeededServiceRestart(t *testing.T) {
	s := &supervisor{ctx: t.Context(), meta: Meta{SessionID: "test"}, state: Status{Control: ControlAgent}}
	_, err := s.dispatch(t.Context(), Request{Op: "run.lease", Session: "test"})
	if err == nil || !strings.Contains(err.Error(), "needs a restart") {
		t.Fatalf("unknown operation error = %v; want it to name the needed service restart", err)
	}
}
