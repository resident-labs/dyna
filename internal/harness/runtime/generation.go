package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

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
	SkillPaths     []string          `json:"skillPaths"`
}

type privateGenerationHome struct {
	Version int    `json:"version"`
	Home    string `json:"home"`
}

func restoreGenerationHome(h, dir string, generation harnessmgr.ConfigGeneration) (harnessmgr.ConfigGeneration, error) {
	// TODO: legacy/shim remove later — drops standalone mode from OpenCode generations made before shared services; remove after those pins expire.
	if h == "opencode" {
		generation.Args = slices.DeleteFunc(generation.Args, func(arg string) bool { return arg == "--standalone" })
	}
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
func materializeNative(h, home, dir, published, cli string, c harnessmgr.HarnessConfig, prefs map[string]any, lookup func(string) (string, bool)) (harnessmgr.ConfigGeneration, error) {
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
		if h == "opencode" {
			if definition.URL != "" {
				row["type"] = "remote"
				row["url"] = definition.URL
			} else {
				row["type"] = "local"
				row["command"] = append([]string{definition.Command}, definition.Args...)
			}
			// Code Mode would hide the server's tools behind execute.
			if entry.Name == dynaMCPName {
				row["codemode"] = false
			}
		}
		// Pi keeps MCP tools from the model unless they are exposed directly
		// (shipped docs/mcp.md: codemode is the default exposure), so the shell
		// tools must be declared like built-in tools.
		if h == "pi" && entry.Name == dynaMCPName {
			row["exposure"] = "direct"
		}
		if h != "opencode" {
			if definition.URL != "" {
				row["url"] = definition.URL
			} else {
				row["command"] = definition.Command
				row["args"] = definition.Args
			}
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
			if h == "opencode" {
				env[name] = "{env:" + harnessmgr.MCPEnvironmentName(name) + "}"
			} else {
				env[name] = "${" + harnessmgr.MCPEnvironmentName(name) + "}"
			}
		}
		if len(env) > 0 {
			if h == "opencode" {
				row["environment"] = env
			} else {
				row["env"] = env
			}
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
			if h == "opencode" {
				row["command"] = append([]string{cli}, args...)
			} else {
				row["command"] = cli
				row["args"] = args
			}
			delete(row, "env")
			delete(row, "environment")
			execSpecs[entry.Name] = harnessmgr.MCPExecSpec{Command: definition.Command, Args: definition.Args, Env: refs}
		}
		servers[entry.Name] = row
	}
	skills := filepath.Join(dir, "skills")
	if err := os.Mkdir(skills, 0700); err != nil {
		return g, err
	}
	paths := []string{}
	for _, entry := range c.Skills {
		if !slices.Contains(entry.EnabledHarnesses, h) {
			continue
		}
		source := entry.Source
		if filepath.Base(entry.Name) != entry.Name || entry.Name == "." || entry.Name == ".." {
			return g, errors.New("invalid skill name")
		}
		target := filepath.Join(skills, entry.Name)
		if err := copyRuntimeSkill(source, target); err != nil {
			return g, err
		}
		paths = append(paths, filepath.Join(published, "skills", entry.Name, "SKILL.md"))
	}
	root := map[string]any{}
	for name, value := range prefs {
		putRuntimePreference(root, name, value)
	}
	if a.MCPKey != "" {
		root[a.MCPKey] = servers
	}
	if h == "opencode" {
		root["mcp"] = map[string]any{"servers": servers}
		root["skills"] = []string{filepath.Join(published, "skills")}
		policies, _ := root["policies"].([]any)
		policies = slices.Clone(policies)
		for _, path := range paths {
			name := filepath.Base(filepath.Dir(path))
			policies = append(policies, map[string]any{"action": "integration.use", "resource": "skill:" + name, "effect": "allow"})
		}
		root["policies"] = policies
		root["update"] = "disable"
	}
	if h == "kimi-code" {
		// ACP has no per-session denylist. Merge only the generated tool
		// policy into the private config; its provider and model sections stay.
		if _, ok := servers[dynaMCPName]; ok {
			tools, _ := root["tools"].(map[string]any)
			if tools == nil {
				tools = map[string]any{}
				root["tools"] = tools
			}
			disabled, _ := tools["disabled"].([]string)
			tools["disabled"] = append(disabled, nativeFeaturePolicies[h].Shell...)
		}
	}
	if h == "grok" {
		root["attribution"] = false
		// Dyna's shell server replaces the native shell; deny it in the same
		// config.toml the generation owns. Never add the denial without the
		// server, or the session loses its shell entirely.
		if _, ok := servers[dynaMCPName]; ok {
			hooks, _ := root["hooks"].(map[string]any)
			if hooks == nil {
				hooks = map[string]any{}
				root["hooks"] = hooks
			}
			entries, _ := hooks["PreToolUse"].([]any)
			hooks["PreToolUse"] = append(entries, GrokShellDenyHook())
		}
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
	if h == "opencode" {
		g.Env["OPENCODE_CONFIG"] = filepath.Join(published, filepath.Base(file))
	}
	if a.ConfigArgs != nil {
		// Muse reads the staged file to construct value arguments; the other
		// adapters only construct paths and must receive published paths.
		if h == "muse" {
			g.Args = a.ConfigArgs(home, file, skills)
		} else {
			g.Args = a.ConfigArgs(home, filepath.Join(published, filepath.Base(file)), filepath.Join(published, "skills"))
		}
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
	if a.ConfigFile != "" && h != "opencode" && h != "muse" {
		g.PartialReasons = append(g.PartialReasons, "Native settings use the shared harness home and are not pinned per generation.")
	}
	if a.SkillDir != "" && !a.SkillsPinned {
		g.PartialReasons = append(g.PartialReasons, "Native account-home skills are not pinned per generation.")
	}
	slices.Sort(g.EnvKeys)
	g.EnvKeys = slices.Compact(g.EnvKeys)
	safeEnv := map[string]string{}
	for k, v := range g.Env {
		if _, isReference := envReferences[k]; !isReference {
			safeEnv[k] = v
		}
	}
	meta := nativeGeneration{Version: 2, Harness: h, Home: home, CLI: generationCLI(servers), Args: g.Args, Env: safeEnv, EnvKeys: g.EnvKeys, PartialReasons: g.PartialReasons, SkillPaths: paths, EnvReferences: envReferences}
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
func copyRuntimeSkill(source, target string) error {
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return errors.New("skill source is unavailable")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(rel string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("skill source could not be read")
		}
		dest := filepath.Join(target, filepath.FromSlash(rel))
		if e.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		if !e.Type().IsRegular() {
			return errors.New("skill resources must be regular files")
		}
		f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("skill resources must be regular files")
		}
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0600|info.Mode().Perm()&0100)
	})
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
