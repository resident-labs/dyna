package model

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestNewProxyKeepsEmbeddedModelCatalogs(t *testing.T) {
	dir := t.TempDir()
	p, err := NewProxy(filepath.Join(dir, "proxy"), filepath.Join(dir, "auth"), "management")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Listener.Close() })
	cfg, err := config.LoadConfig(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Catalog != "embed" || cfg.Models.CodexCatalog != "embed" {
		t.Fatalf("catalog sources = %+v, want embedded general and Codex catalogs", cfg.Models)
	}
	if cfg.Models.DevinCatalog != "" {
		t.Fatalf("Devin catalog source = %q, want unchanged default", cfg.Models.DevinCatalog)
	}
}
