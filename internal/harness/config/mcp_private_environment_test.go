package config

import (
	"context"
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

func TestMCPPrivateEnvironmentExecHelper(t *testing.T) {
	if os.Getenv("HOME_MCP_EXEC_HELPER") != "1" {
		return
	}
	if err := RunGeneratedMCP(os.Getenv("HOME_MCP_GENERATION"), "tools"); err != nil {
		t.Fatal(err)
	}
}

func TestMCPReferencesSurvivePrivateOverridesAndResume(t *testing.T) {
	m, account, root := editManager(t)
	keys := []string{"BASH_ENV", "ENV", "ZDOTDIR", "GIT_CONFIG_GLOBAL", "GOCACHE", "GH_CONFIG_DIR", "NODE_COMPILE_CACHE"}
	server := filepath.Join(t.TempDir(), "server")
	script := "#!/bin/sh\n"
	for _, key := range keys {
		script += "printf '%s\\n' \"$" + key + "\"\n"
	}
	mustEdit(t, fsutil.WriteExecutable(server, []byte(script), 0700))
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tools", MCPDefinition: MCPDefinition{Command: server, EnvKeys: keys}, EnabledHarnesses: []string{"codex"}}))
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
	generation, err := m.GenerateWithEnv("codex", account, root, lookup("initial"))
	mustEdit(t, err)
	private, err := home.Prepare(account, "/fake/dyna", []string{"HOME=" + m.Home, "PATH=" + os.Getenv("PATH")})
	mustEdit(t, err)
	for _, suffix := range []string{"initial", "resumed"} {
		t.Run(suffix, func(t *testing.T) {
			current := generation
			if suffix == "resumed" {
				current, err = LoadGenerationWithEnv(generation.Directory, lookup(suffix))
				mustEdit(t, err)
			}
			env := map[string]string{"HOME_MCP_EXEC_HELPER": "1", "HOME_MCP_GENERATION": generation.Directory}
			for key, value := range current.Env {
				env[key] = value
			}
			// This is the private runtime's final merge order. Server references
			// must survive while the harness keeps its own cleared startup inputs.
			for key, value := range private {
				env[key] = value
			}
			expected := []string{}
			for _, key := range keys {
				alias := MCPEnvironmentName(key)
				want, _ := lookup(suffix)(key)
				if alias == key || env[alias] != want || env[key] == want {
					t.Fatalf("%s lost its server scope after private merge", key)
				}
				expected = append(expected, want)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPPrivateEnvironmentExecHelper$")
			for key, value := range env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != strings.Join(expected, "\n")+"\n" {
				t.Fatalf("server did not receive referenced values: %v: %s", err, output)
			}
		})
	}
}
