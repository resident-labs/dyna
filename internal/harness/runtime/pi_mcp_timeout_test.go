package runtime

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/shellmcp"
)

// piServerDoc is one entry of Pi's mcp.json in the shape these tests read.
type piServerDoc struct {
	Command  string   `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	Exposure string   `json:"exposure,omitempty"`
	Timeout  int      `json:"timeout,omitempty"`
}

type piMCPDoc struct {
	MCP map[string]piServerDoc `json:"mcpServers"`
}

// Pi reads a per-server request timeout and exposure from mcp.json. Dyna's
// shell server waits up to ten minutes for a job, so its timeout must outlast
// that wait, and a catalog entry must set both fields itself.
func TestPiMCPTimeoutAndExposure(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	slow := harnessmgr.ManagedMCP{Name: "slow", MCPDefinition: harnessmgr.MCPDefinition{
		Command: "slow", TimeoutSeconds: 300, Exposure: harnessmgr.MCPExposureDeferred,
	}, EnabledHarnesses: []string{"pi"}}
	if err := r.Config.AddMCP(owner, slow); err != nil {
		t.Fatal(err)
	}
	g := buildGeneration(t, r, "pi", home, id)
	raw, err := os.ReadFile(filepath.Join(g.Directory, "pi-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root piMCPDoc
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if entry := root.MCP["slow"]; entry.Timeout != 300 || entry.Exposure != "deferred" {
		t.Fatalf("catalog server = %+v, want timeout 300 and deferred exposure in %s", entry, raw)
	}
	wantShell := int(shellmcp.MaxRequestTimeout.Seconds())
	if entry := root.MCP[dynaMCPName]; entry.Timeout != wantShell || entry.Exposure != "direct" {
		t.Fatalf("dyna shell server = %+v, want timeout %d and direct exposure in %s", entry, wantShell, raw)
	}
}

// A session pinned to a Pi generation from before the shell timeout keeps that
// generation until the pin is refused. The inputs version refuses it, so the
// resumed turn publishes a config whose shell server outlasts its longest wait.
func TestOldPiPinIsRebuiltWithTheShellTimeout(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	_, pin, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Model the pin a build before the timeout would carry: its own content
	// address, the previous inputs version, and a shell server without a
	// timeout. Its lifecycle hash is rebuilt, so only the version marks it as
	// outdated, exactly as it would be under the older build.
	oldDir := filepath.Join(r.Generations, generationPrefix+strings.Repeat("ab", 32))
	copyGeneration(t, pin.Generation, oldDir)
	mcpPath := filepath.Join(oldDir, "pi-mcp.json")
	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc piMCPDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	shell := doc.MCP[dynaMCPName]
	shell.Timeout = 0
	doc.MCP[dynaMCPName] = shell
	if raw, err = json.MarshalIndent(doc, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	// The inputs version of the build that wrote pins without a timeout.
	const previousInputsVersion = 10
	oldFeatures, err := json.MarshalIndent(nativeFeatureSnapshot{previousInputsVersion, "pi", nativeFeaturePolicies["pi"]}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, nativeFeatureFile), append(oldFeatures, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(oldDir, generationMetadata)
	var meta generationOwnership
	if err := readGenerationJSON(metaPath, &meta); err != nil {
		t.Fatal(err)
	}
	meta.InputHash = strings.TrimPrefix(filepath.Base(oldDir), generationPrefix)
	if meta.PayloadHash, err = generationTreeHash(oldDir, true); err != nil {
		t.Fatal(err)
	}
	if raw, err = json.MarshalIndent(meta, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	pin.Generation = oldDir

	_, next, err := r.Prepare(context.Background(), p, &pin)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(next.Generation, "pi-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := int(shellmcp.MaxRequestTimeout.Seconds())
	if got := doc.MCP[dynaMCPName].Timeout; got != want {
		t.Fatalf("resumed shell server timeout = %d, want %d in %s", got, want, raw)
	}
}

// copyGeneration copies a published generation tree for a pin fixture.
func copyGeneration(t *testing.T, from, to string) {
	t.Helper()
	if err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}); err != nil {
		t.Fatal(err)
	}
}
