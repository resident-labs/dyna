package config

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/home"
)

func TestMCPReferencesSurvivePrivateOverridesAndResume(t *testing.T) {
	m, account, root := editManager(t)
	keys := []string{"BASH_ENV", "ENV", "ZDOTDIR", "GIT_CONFIG_GLOBAL", "GOCACHE", "GH_CONFIG_DIR", "NODE_COMPILE_CACHE"}
	server := filepath.Join(t.TempDir(), "server")
	script := "#!/bin/sh\n"
	for _, key := range keys {
		script += "printf '%s\\n' \"$" + key + "\"\n"
	}
	mustEdit(t, fsutil.WriteExecutable(server, []byte(script), 0700))
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tools", MCPDefinition: MCPDefinition{Command: server, EnvKeys: keys}, EnabledHarnesses: []string{"claude"}}))
	lookup := func(suffix string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			if key == "DYNA_BIN" {
				return "/fake/dyna", true
			}
			for _, name := range keys {
				if key == name {
					return "server-" + key + "-" + suffix, true
				}
			}
			return "", false
		}
	}
	generation, err := m.GenerateWithEnv("claude-code", account, root, lookup("initial"))
	mustEdit(t, err)
	private, err := home.Prepare(account, "/fake/dyna", []string{"HOME=" + m.Home, "PATH=" + os.Getenv("PATH")})
	mustEdit(t, err)
	var native struct {
		Servers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	mustEdit(t, json.Unmarshal(fileBytes(t, filepath.Join(generation.Directory, "mcp.json")), &native))
	if len(native.Servers["tools"].Env) != len(keys) {
		t.Fatalf("generated server has %d environment references, want %d", len(native.Servers["tools"].Env), len(keys))
	}
	for _, suffix := range []string{"initial", "resumed"} {
		t.Run(suffix, func(t *testing.T) {
			current := generation
			if suffix == "resumed" {
				current, err = LoadGenerationWithEnv(generation.Directory, lookup(suffix))
				mustEdit(t, err)
			}
			// This is the private runtime's final merge order. Server references
			// must survive while the harness keeps its own cleared startup inputs.
			env := map[string]string{}
			for key, value := range current.Env {
				env[key] = value
			}
			for key, value := range private {
				env[key] = value
			}
			launch := []string{}
			expected := []string{}
			for _, key := range keys {
				alias := MCPEnvironmentName(key)
				want, _ := lookup(suffix)(key)
				if alias == key || env[alias] != want || env[key] == want {
					t.Fatalf("%s lost its server scope after private merge", key)
				}
				// Claude replaces ${alias} with the value of that variable at launch.
				reference := native.Servers["tools"].Env[key]
				if reference != "${"+alias+"}" {
					t.Fatalf("%s native reference is %q, want ${%s}", key, reference, alias)
				}
				launch = append(launch, key+"="+env[alias])
				expected = append(expected, want)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, server)
			cmd.Env = launch
			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != strings.Join(expected, "\n")+"\n" {
				t.Fatalf("server did not receive referenced values: %v: %s", err, output)
			}
		})
	}
}
