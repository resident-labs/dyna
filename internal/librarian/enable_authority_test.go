package librarian

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/store/storetest"
)

func TestLibrarianServiceAuthority(t *testing.T) {
	storetest.Isolate(t)
	// The owner process must not grant its authority to a session argument.
	for _, role := range []authority.Role{authority.Session, authority.Worker} {
		svc := Service{}
		actor := authority.Actor{Role: role, Session: "ses-test"}
		if _, err := svc.Enable(t.Context(), actor, "missing", "missing", "dyna"); err == nil || !strings.HasPrefix(err.Error(), "library enable is ") {
			t.Fatalf("enable: %v", err)
		}
		if _, err := svc.Disable(t.Context(), actor, "missing"); err == nil || !strings.HasPrefix(err.Error(), "library disable is ") {
			t.Fatalf("disable: %v", err)
		}
	}
}

func TestLibrarianOwnerAndRootAuthority(t *testing.T) {
	storetest.Isolate(t)
	for _, role := range []authority.Role{authority.Owner, authority.Root} {
		svc := Service{}
		missing := filepath.Join(t.TempDir(), "missing")
		actor := authority.Actor{Role: role}
		if _, err := svc.Enable(t.Context(), actor, missing, "missing", "dyna"); err == nil || !strings.Contains(err.Error(), "git rev-parse") {
			t.Fatalf("enable validation: %v", err)
		}
		if _, err := svc.Disable(t.Context(), actor, missing); err == nil || !strings.Contains(err.Error(), "git rev-parse") {
			t.Fatalf("disable validation: %v", err)
		}
	}
}
