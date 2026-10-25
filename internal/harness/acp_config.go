package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dyna-agent/internal/acp"
	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
)

// validateAntigravityACPLogin never starts OAuth from a worker turn.
func validateAntigravityACPLogin(home string) error {
	if home == "" {
		return errors.New("Antigravity ACP needs the private account HOME")
	}
	token := filepath.Join(home, "antigravity-acp", "acp_token.json")
	info, err := os.Lstat(token)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("Antigravity ACP login is missing or not private at %s; complete the Antigravity ACP account login before starting a worker", token)
	}
	if err := os.MkdirAll(filepath.Join(home, "antigravity-acp", "tmp"), 0700); err != nil {
		return err
	}
	return nil
}

// validateAntigravityACPBundle keeps immutable CLI-only installs intact.
// Their replacement goes through the ordinary unused-bundle removal command.
func validateAntigravityACPBundle(cli string) error {
	for _, name := range []string{"agy_acp_server.par", "localharness_external"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(cli), name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("Antigravity runtime is missing its pinned ACP companion %s; remove the unused CLI-only bundle with dyna harness rm antigravity --version <version> --confirm, then run dyna harness install antigravity", name)
		}
	}
	return nil
}

// antigravityMeta is Antigravity's session _meta extension, which disables the
// native tools Dyna already serves itself.
type antigravityMeta struct {
	Antigravity antigravitySessionTools `json:"agy"`
}

type antigravitySessionTools struct {
	DisabledTools []string `json:"disabledTools"`
}

// antigravitySessionMeta builds the raw extension data for a session request.
func antigravitySessionMeta(disabledTools []string) json.RawMessage {
	raw, err := json.Marshal(antigravityMeta{Antigravity: antigravitySessionTools{DisabledTools: disabledTools}})
	if err != nil {
		return nil
	}
	return raw
}

// acpMCPServers carries the immutable catalog into Antigravity's ACP session.
// Grok reads its generated account config directly.
func acpMCPServers(p profile.Profile, launchEnv []string) ([]acp.McpServer, error) {
	if p.Harness != profile.HarnessAntigravity && p.Harness != profile.HarnessKimiCode {
		return nil, nil
	}
	generation, ok := argumentValue(p.ExtraArgs, "--add-dir")
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(generation, "settings.json"))
	if err != nil {
		return nil, fmt.Errorf("read Antigravity ACP catalog: %w", err)
	}
	var config struct {
		Servers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
			URL     string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode Antigravity ACP catalog: %w", err)
	}
	launchValues := map[string]string{}
	for _, pair := range launchEnv {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			launchValues[key] = value
		}
	}
	names := make([]string, 0, len(config.Servers))
	for name := range config.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]acp.McpServer, 0, len(names))
	for _, name := range names {
		if p.Harness == profile.HarnessKimiCode && name != "dyna" {
			continue
		}
		server := config.Servers[name]
		if server.URL != "" {
			result = append(result, acp.McpServer{Name: name, URL: server.URL})
			continue
		}
		if server.Command == "" {
			return nil, fmt.Errorf("Antigravity ACP catalog server %q needs a command or URL", name)
		}
		env := map[string]string{}
		// Dyna's shell and its child jobs must see the final launch environment.
		if name == "dyna" {
			for key, value := range launchValues {
				env[key] = value
			}
		}
		// Generated wrappers read their original environment names from aliases.
		if len(server.Args) == 4 && server.Args[0] == "mcp" && server.Args[1] == "serve" && server.Args[2] == generation && server.Args[3] == name {
			spec, err := harnessconfig.ReadMCPExecSpec(generation, name)
			if err != nil {
				return nil, err
			}
			for _, reference := range spec.Env {
				value, ok := launchValues[reference]
				if !ok {
					return nil, fmt.Errorf("Antigravity ACP catalog server %q has an unavailable environment reference", name)
				}
				env[reference] = value
			}
		}
		for key, value := range server.Env {
			env[key] = value
		}
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		vars := make([]acp.EnvVariable, 0, len(keys))
		for _, key := range keys {
			vars = append(vars, acp.EnvVariable{Name: key, Value: env[key]})
		}
		args := server.Args
		if args == nil {
			args = []string{}
		}
		result = append(result, acp.McpServer{Name: name, Command: server.Command, Args: args, Env: vars})
	}
	return result, nil
}
