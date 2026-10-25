package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

func TestCatalogChecksAuthorityBeforeValidation(t *testing.T) {
	manager := &ConfigManager{Path: filepath.Join(t.TempDir(), "absent", "catalog.json")}
	for _, role := range []authority.Role{authority.Session, authority.Worker, ""} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		for _, err := range []error{
			manager.AddMCP(ctx, ManagedMCP{}), manager.AddSkill(ctx, ManagedSkill{}),
			manager.Remove(ctx, "mcp", "", ""), manager.Remove(ctx, "skill", "", "claude-code"),
			manager.SetEnabled(ctx, "mcp", "", "", true), manager.SetEnabled(ctx, "skill", "", "", false),
		} {
			if err == nil || !strings.Contains(err.Error(), "is owner or root only") {
				t.Fatalf("role %q reached config validation: %v", role, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Dir(manager.Path)); !os.IsNotExist(err) {
		t.Fatalf("refused edits changed the filesystem: %v", err)
	}
}
