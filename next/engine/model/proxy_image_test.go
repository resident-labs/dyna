package model

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The proxy's config keeps Codex requests free of an injected image_generation tool, whose settings failed sol turns at
// the model call, while the Image API endpoints stay available.
func TestNewProxyDisablesInjectedImageGeneration(t *testing.T) {
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
	if got := cfg.DisableImageGeneration.String(); got != "chat" {
		t.Fatalf("disable-image-generation is %q, want chat", got)
	}
}
