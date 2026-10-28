package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"github.com/pelletier/go-toml/v2"
)

type nativeGeneration struct {
	EnvReferences  map[string]string `json:"envReferences,omitempty"`
	Version        int               `json:"version"`
	Harness        string            `json:"harness"`
	Home           string            `json:"home"`
	CLI            string            `json:"cli,omitempty"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"` // native paths/reference syntax only; no values
	EnvKeys        []string          `json:"envKeys"`
	PartialReasons []string          `json:"partialReasons"`
}

type privateGenerationHome struct {
	Version int    `json:"version"`
	Home    string `json:"home"`
}

func restoreGenerationHome(h, dir string, generation harnessmgr.ConfigGeneration) (harnessmgr.ConfigGeneration, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "private-home.json"))
	if err != nil {
		return generation, err
	}
	var scope privateGenerationHome
	if json.Unmarshal(raw, &scope) != nil || scope.Version != 1 || safeAccountPath(scope.Home) != nil || scope.Home == "" {
		return generation, errors.New("invalid private generation HOME")
	}
	if generation.Env == nil {
		generation.Env = map[string]string{}
	}
	for key, value := range harnesshome.Roots(scope.Home) {
		generation.Env[key] = value
	}
	return generation, nil
}

// materializeNative writes a captured catalog to staging. All retained references
// use published. Shared-home publication happens only after the atomic rename.
func materializeNative(h, home, dir, published, cli string, c harnessmgr.HarnessConfig, prefs map[string]any, piReserves map[string]int64, lookup func(string) (string, bool)) (harnessmgr.ConfigGeneration, error) {
	a, err := Adapter(h)
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	g := harnessmgr.ConfigGeneration{ID: filepath.Base(published), Directory: published, Env: map[string]string{}, Args: []string{}, EnvKeys: []string{}, PartialReasons: append([]string{}, a.PartialReasons...)}
	servers := map[string]any{}
	envReferences := map[string]string{}
	execSpecs := map[string]harnessmgr.MCPExecSpec{}
	for _, entry := range c.MCP {
		if !slices.Contains(entry.EnabledHarnesses, h) {
			continue
		}
		definition := entry.MCPDefinition
		row := map[string]any{}
		// Pi keeps MCP tools from the model unless they are exposed directly
		// (shipped docs/mcp.md: codemode is the default exposure), so the shell
		// tools must be declared like built-in tools. A catalog entry names its
		// own exposure for its own tools.
		if h == "pi" {
			exposure := definition.Exposure
			if entry.Name == dynaMCPName {
				exposure = harnessmgr.MCPExposureDirect
			}
			if exposure != "" {
				row["exposure"] = string(exposure)
			}
			if definition.TimeoutSeconds > 0 {
				row["timeout"] = definition.TimeoutSeconds
			}
		}
		if definition.URL != "" {
			row["url"] = definition.URL
		} else {
			row["command"] = definition.Command
			row["args"] = definition.Args
		}
		env := map[string]string{}
		for _, name := range definition.EnvKeys {
			value, found := lookup(name)
			if !found {
				return g, fmt.Errorf("MCP environment reference %s is missing", name)
			}
			alias := harnessmgr.MCPEnvironmentName(name)
			g.Env[alias] = value
			envReferences[alias] = name
			g.EnvKeys = append(g.EnvKeys, name)
			env[name] = "${" + harnessmgr.MCPEnvironmentName(name) + "}"
		}
		if len(env) > 0 {
			row["env"] = env
		}
		if definition.URL == "" && len(definition.EnvKeys) > 0 {
			refs := map[string]string{}
			for _, key := range definition.EnvKeys {
				refs[key] = harnessmgr.MCPEnvironmentName(key)
			}
			if cli == "" {
				return g, errors.New("Dyna CLI is required for MCP environment references")
			}
			args := []string{"mcp", "serve", published, entry.Name}
			row["command"] = cli
			row["args"] = args
			delete(row, "env")
			delete(row, "environment")
			execSpecs[entry.Name] = harnessmgr.MCPExecSpec{Command: definition.Command, Args: definition.Args, Env: refs}
		}
		servers[entry.Name] = row
	}
	root := map[string]any{}
	for name, value := range prefs {
		putRuntimePreference(root, name, value)
	}
	if len(piReserves) > 0 {
		compaction, ok := root["compaction"].(map[string]any)
		if !ok {
			if _, exists := root["compaction"]; exists {
				return g, errors.New("Pi compaction settings must be an object")
			}
			compaction = map[string]any{}
			root["compaction"] = compaction
		}
		overrides, ok := compaction["modelOverrides"].(map[string]any)
		if !ok {
			if _, exists := compaction["modelOverrides"]; exists {
				return g, errors.New("Pi compaction model overrides must be an object")
			}
			overrides = map[string]any{}
			compaction["modelOverrides"] = overrides
		}
		for model, reserve := range piReserves {
			entry, ok := overrides[model].(map[string]any)
			if !ok {
				if _, exists := overrides[model]; exists {
					return g, fmt.Errorf("Pi compaction override for %q must be an object", model)
				}
				entry = map[string]any{}
				overrides[model] = entry
			}
			if _, exists := entry["reserveTokens"]; !exists {
				entry["reserveTokens"] = reserve
			}
		}
	}
	if a.MCPKey != "" {
		root[a.MCPKey] = servers
	}
	var raw []byte
	if a.ConfigFormat == "toml" {
		raw, err = toml.Marshal(root)
	} else {
		raw, err = json.MarshalIndent(root, "", "  ")
	}
	if err != nil {
		return g, errors.New("private native config could not be encoded")
	}
	file := filepath.Join(dir, "settings.json")
	if a.ConfigFormat == "toml" {
		file = filepath.Join(dir, "settings.toml")
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		return g, err
	}
	if len(execSpecs) > 0 {
		if err := writeNativeGeneration(filepath.Join(dir, "mcp-exec.json"), execSpecs); err != nil {
			return g, err
		}
	}
	nativeEnv := map[string]string{}
	if a.NativeEnv != nil {
		nativeEnv = a.NativeEnv(home)
	}
	for k, v := range nativeEnv {
		if k == "HOME" || strings.HasPrefix(k, "XDG_") {
			return g, errors.New("runtime adapter cannot change HOME or XDG")
		}
		g.Env[k] = v
	}
	for k, v := range harnesshome.Roots(home) {
		g.Env[k] = v
	}
	if a.ConfigArgs != nil {
		// Adapters construct paths from the published generation.
		g.Args = a.ConfigArgs(home, filepath.Join(published, filepath.Base(file)))
	}
	if h == "pi" {
		mcpRaw, e := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
		if e != nil {
			return g, e
		}
		if e := os.WriteFile(filepath.Join(dir, "pi-mcp.json"), mcpRaw, 0600); e != nil {
			return g, e
		}
	}
	if a.ConfigFile != "" {
		g.PartialReasons = append(g.PartialReasons, "Native settings use the shared harness home and are not pinned per generation.")
	}
	slices.Sort(g.EnvKeys)
	g.EnvKeys = slices.Compact(g.EnvKeys)
	safeEnv := map[string]string{}
	for k, v := range g.Env {
		if _, isReference := envReferences[k]; !isReference {
			safeEnv[k] = v
		}
	}
	meta := nativeGeneration{Version: 2, Harness: h, Home: home, CLI: generationCLI(servers), Args: g.Args, Env: safeEnv, EnvKeys: g.EnvKeys, PartialReasons: g.PartialReasons, EnvReferences: envReferences}
	if err := writeNativeGeneration(filepath.Join(dir, "native-generation.json"), meta); err != nil {
		return g, err
	}
	return g, nil
}
func writeNativeGeneration(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

// generationCLI names the Dyna executable this generation's MCP servers run.
// A pin whose command no longer launches must be rebuilt, or its next turn has
// no Dyna tools.
func generationCLI(servers map[string]any) string {
	row, ok := servers[dynaMCPName].(map[string]any)
	if !ok {
		return ""
	}
	command, _ := row["command"].(string)
	return command
}

func loadNativeGeneration(h, dir string, lookup func(string) (string, bool)) (harnessmgr.ConfigGeneration, error) {
	g, err := readNativeGeneration(h, dir, lookup)
	if err != nil {
		return g, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, nativeFeatureFile))
	// TODO: legacy/shim remove later — pins from before native feature policy have no snapshot; Prepare rebuilds them on resume.
	if os.IsNotExist(err) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	var snapshot nativeFeatureSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Harness == "" {
		return g, errors.New("invalid native feature policy snapshot")
	}
	applyNativeFeaturePolicy(snapshot.Harness, &g)
	return g, nil
}

func readNativeGeneration(h, dir string, lookup func(string) (string, bool)) (harnessmgr.ConfigGeneration, error) {
	if ownedGenerationName(filepath.Base(dir)) {
		if _, err := validateOwnedGeneration(dir); err != nil {
			return harnessmgr.ConfigGeneration{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "native-generation.json"))
	if err != nil {
		generation, err := harnessmgr.LoadGenerationWithEnv(dir, lookup)
		if err != nil {
			return generation, err
		}
		return restoreGenerationHome(h, dir, generation)
	}
	var m nativeGeneration
	if json.Unmarshal(raw, &m) != nil || m.Version != 2 {
		return harnessmgr.ConfigGeneration{}, errors.New("invalid native generation manifest")
	}
	out := harnessmgr.ConfigGeneration{ID: filepath.Base(dir), Directory: dir, CLI: m.CLI, Args: m.Args, Env: m.Env, EnvKeys: m.EnvKeys, PartialReasons: m.PartialReasons}
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	for _, key := range m.EnvKeys {
		v, ok := lookup(key)
		if !ok {
			return out, fmt.Errorf("MCP environment reference %s is missing", key)
		}
		alias := harnessmgr.MCPEnvironmentName(key)
		if source, ok := m.EnvReferences[alias]; ok && source != key {
			return out, errors.New("invalid native environment reference")
		}
		out.Env[alias] = v
	}
	return restoreGenerationHome(h, dir, out)
}
func putRuntimePreference(root map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := root[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			root[p] = next
		}
		root = next
	}
	root[parts[len(parts)-1]] = value
}
