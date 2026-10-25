package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"dyna-agent/internal/harness/home"
)

type MCPExecSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// MCPEnvironmentName keeps MCP credentials from changing the harness account or
// its native paths. The server receives its original names after translation.
func MCPEnvironmentName(key string) string {
	if home.PrivateEnvironmentKey(key) || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "OPENCODE_") || strings.HasPrefix(key, "CURSOR_") || strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "GEMINI_") || strings.HasPrefix(key, "GROK_") || strings.HasPrefix(key, "MUSE_") || key == "OPENAI_API_KEY" || key == "META_API_KEY" || key == "DBUS_SESSION_BUS_ADDRESS" || strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GOOGLE_") || strings.HasPrefix(key, "XAI_") || strings.HasPrefix(key, "TBH_") || strings.HasPrefix(key, "PI_") {
		sum := sha256.Sum256([]byte(key))
		return "DYNA_MCP_" + strings.ToUpper(hex.EncodeToString(sum[:8]))
	}
	return key
}
func dynaMCPExecutable(lookup func(string) (string, bool)) (string, error) {
	if lookup != nil {
		if path, ok := lookup("DYNA_BIN"); ok && filepath.IsAbs(path) {
			return path, nil
		}
	}
	path, err := exec.LookPath("dyna")
	if err != nil {
		return "", errors.New("Dyna CLI is required to isolate MCP environment references")
	}
	return path, nil
}

// ReadMCPExecSpec reads one immutable generated server definition.
func ReadMCPExecSpec(directory, name string) (MCPExecSpec, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return MCPExecSpec{}, errors.New("invalid MCP generation path")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "mcp-exec.json"))
	if err != nil {
		return MCPExecSpec{}, errors.New("MCP generation is unavailable")
	}
	specs := map[string]MCPExecSpec{}
	if json.Unmarshal(raw, &specs) != nil {
		return MCPExecSpec{}, errors.New("invalid MCP execution manifest")
	}
	spec, ok := specs[name]
	if !ok || spec.Command == "" {
		return MCPExecSpec{}, errors.New("MCP execution entry is unavailable")
	}
	return spec, nil
}

// RunGeneratedMCP preserves stdio and replaces this process with the selected
// catalog server. Its manifest contains names and references, never values.
func RunGeneratedMCP(directory, name string) error {
	spec, err := ReadMCPExecSpec(directory, name)
	if err != nil {
		return err
	}
	env := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok && !strings.HasPrefix(key, "DYNA_MCP_") {
			env[key] = value
		}
	}
	for key, reference := range spec.Env {
		value, ok := os.LookupEnv(reference)
		if !ok {
			return errors.New("MCP environment reference is unavailable")
		}
		env[key] = value
	}
	pairs := []string{}
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	executable := spec.Command
	if !filepath.IsAbs(executable) {
		path, err := exec.LookPath(executable)
		if err != nil {
			return errors.New("MCP executable is unavailable")
		}
		executable = path
	}
	return syscall.Exec(executable, append([]string{spec.Command}, spec.Args...), pairs)
}
