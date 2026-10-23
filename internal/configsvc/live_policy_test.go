package configsvc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
)

func TestLiveProfileCheckRefusesBeforeConfigRead(t *testing.T) {
	isolate(t)
	path := profile.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("invalid profile config")
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	for _, role := range []authority.Role{authority.Session, authority.Worker, ""} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		for _, check := range []func() error{
			func() error { _, err := CheckProfile(ctx, "missing", true); return err },
			func() error { _, err := (&Service{}).ConfigCheckProfile(ctx, "missing", true); return err },
		} {
			if err := check(); err == nil || !strings.Contains(err.Error(), "ConfigCheckProfile is owner or root only") {
				t.Errorf("role %q reached config read: %v", role, err)
			}
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, invalid) {
		t.Fatalf("refusal changed profiles: %v", err)
	}
}

func TestStaticProfileChecksKeepCallerAccess(t *testing.T) {
	isolate(t)
	seed(t, mock("observed"))
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker, ""} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		got, err := (&Service{}).ConfigCheckProfile(ctx, "observed", false)
		if err != nil || got.Name != "observed" {
			t.Fatalf("static check as %q: %+v %v", role, got, err)
		}
	}
}
