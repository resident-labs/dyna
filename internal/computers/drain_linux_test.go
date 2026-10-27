//go:build linux

package computers

import (
	"context"
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
