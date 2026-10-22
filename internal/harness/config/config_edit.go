package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
)

var configName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var configEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateConfigName(name string) error {
	if !configName.MatchString(name) {
		return errors.New("invalid config name; use letters, numbers, dots, underscores, or hyphens")
	}
	return nil
}
func normalizeEnabled(hs []string) ([]string, error) {
	out := []string{}
	for _, h := range hs {
		if h == "" {
			return nil, errors.New("empty harness ID")
		}
		selected, err := selectedHarnesses(h)
		if err != nil {
			return nil, errors.New("unsupported harness ID")
		}
		if !slices.Contains(out, selected[0]) {
			out = append(out, selected[0])
		}
	}
	sort.Strings(out)
	return out, nil
}
func validateMCPDefinition(d MCPDefinition) error {
	// Check the command on its own too: commands can themselves contain URLs.
	// Alone, a path that names a credentials folder is not a flag with a value.
	if !safeMCPDefinition(d) || !safeMCPDefinition(MCPDefinition{Args: []string{d.Command}}) {
		return errors.New("MCP definition contains embedded credentials; use environment key references")
	}
	if (d.Command == "") == (d.URL == "") {
		return errors.New("MCP definition requires exactly one command or URL")
	}
	if strings.ContainsAny(d.Command, "\x00\r\n") {
		return errors.New("invalid MCP command")
	}
	for _, arg := range d.Args {
		if strings.ContainsRune(arg, 0) {
			return errors.New("invalid MCP argument")
		}
	}
	if d.URL != "" {
		u, err := url.Parse(d.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Fragment != "" {
			return errors.New("MCP URL requires an HTTP or HTTPS address without a fragment")
		}
		if len(d.Args) > 0 || len(d.EnvKeys) > 0 {
			return errors.New("URL MCP definitions do not support command arguments or environment keys")
		}
	}
	for _, key := range d.EnvKeys {
		if !configEnvKey.MatchString(key) || reservedRuntimeEnvKey(key) {
			return errors.New("invalid environment key; values must not be stored in the catalog")
		}
	}
	return nil
}
func validateSkillSource(source string) error {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return errors.New("skill source must be a clean absolute directory path")
	}
	resolved, resolveErr := filepath.EvalSymlinks(source)
	accountRoot := store.DefaultStore().Root
	if resolvedRoot, err := filepath.EvalSymlinks(accountRoot); err == nil {
		accountRoot = resolvedRoot
	}
	if resolveErr != nil || pathWithin(accountRoot, resolved) {
		return errors.New("skill sources cannot include saved native accounts")
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return errors.New("skill source must be an existing directory")
	}
	info, err = os.Stat(filepath.Join(source, "SKILL.md"))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("skill source must contain a regular SKILL.md file")
	}
	return nil
}

// AddMCP changes only Dyna's catalog.
func (m *ConfigManager) AddMCP(ctx context.Context, x ManagedMCP) error {
	if err := authority.Check("mcp add", authority.Of(ctx), nil); err != nil {
		return err
	}
	if err := validateConfigName(x.Name); err != nil {
		return err
	}
	if err := validateMCPDefinition(x.MCPDefinition); err != nil {
		return err
	}
	hs, err := normalizeEnabled(x.EnabledHarnesses)
	if err != nil {
		return err
	}
	x.EnabledHarnesses = hs
	return m.update(func(c *HarnessConfig) error {
		if slices.ContainsFunc(c.MCP, func(v ManagedMCP) bool { return v.Name == x.Name }) {
			return errors.New("MCP name already exists")
		}
		c.MCP = append(c.MCP, x)
		return nil
	})
}
func (m *ConfigManager) AddSkill(ctx context.Context, x ManagedSkill) error {
	if err := authority.Check("skill add", authority.Of(ctx), nil); err != nil {
		return err
	}
	if err := validateConfigName(x.Name); err != nil {
		return err
	}
	if err := validateSkillSource(x.Source); err != nil {
		return err
	}
	hs, err := normalizeEnabled(x.EnabledHarnesses)
	if err != nil {
		return err
	}
	x.EnabledHarnesses = hs
	return m.update(func(c *HarnessConfig) error {
		if slices.ContainsFunc(c.Skills, func(v ManagedSkill) bool { return v.Name == x.Name }) {
			return errors.New("skill name already exists")
		}
		c.Skills = append(c.Skills, x)
		return nil
	})
}

// Remove without a harness removes the catalog entry. A scoped removal removes
// its enablement.
func (m *ConfigManager) Remove(ctx context.Context, kind, name, harness string) error {
	if err := validKind(kind); err != nil {
		return err
	}
	if err := authority.Check(kind+" rm", authority.Of(ctx), nil); err != nil {
		return err
	}
	if harness != "" {
		return m.setEnabled(kind, name, harness, false)
	}
	if err := validateConfigName(name); err != nil {
		return err
	}
	return m.update(func(c *HarnessConfig) error {
		if kind == "mcp" {
			i := slices.IndexFunc(c.MCP, func(x ManagedMCP) bool { return x.Name == name })
			if i < 0 {
				return errors.New("MCP name does not exist")
			}
			c.MCP = slices.Delete(c.MCP, i, i+1)
		} else {
			i := slices.IndexFunc(c.Skills, func(x ManagedSkill) bool { return x.Name == name })
			if i < 0 {
				return errors.New("skill name does not exist")
			}
			c.Skills = slices.Delete(c.Skills, i, i+1)
		}
		return nil
	})
}
func (m *ConfigManager) SetEnabled(ctx context.Context, kind, name, harness string, enabled bool) error {
	if err := validKind(kind); err != nil {
		return err
	}
	action := kind + " disable"
	if enabled {
		action = kind + " enable"
	}
	if err := authority.Check(action, authority.Of(ctx), nil); err != nil {
		return err
	}
	return m.setEnabled(kind, name, harness, enabled)
}

func (m *ConfigManager) setEnabled(kind, name, harness string, enabled bool) error {
	if err := validateConfigName(name); err != nil {
		return err
	}
	hs, err := selectedHarnesses(harness)
	if err != nil {
		return errors.New("unsupported harness ID")
	}
	return m.update(func(c *HarnessConfig) error {
		var selected *[]string
		if kind == "mcp" {
			i := slices.IndexFunc(c.MCP, func(x ManagedMCP) bool { return x.Name == name })
			if i < 0 {
				return errors.New("MCP name does not exist")
			}
			x := &c.MCP[i]
			selected = &x.EnabledHarnesses
			if enabled {
				if err := validateMCPDefinition(x.MCPDefinition); err != nil {
					return err
				}
			}
		} else {
			i := slices.IndexFunc(c.Skills, func(x ManagedSkill) bool { return x.Name == name })
			if i < 0 {
				return errors.New("skill name does not exist")
			}
			x := &c.Skills[i]
			selected = &x.EnabledHarnesses
			if enabled {
				if err := validateSkillSource(x.Source); err != nil {
					return err
				}
			}
		}
		for _, h := range hs {
			*selected = slices.DeleteFunc(*selected, func(v string) bool { return v == h })
			if enabled {
				*selected = append(*selected, h)
			}
		}
		sort.Strings(*selected)
		return nil
	})
}

// ConfigGeneration is a launch snapshot. The caller retains it for the session
// lifetime. Env can contain secrets and must not be logged or persisted.
type ConfigGeneration struct {
	ID        string
	Directory string
	Args      []string
	Env       map[string]string `json:"-"`
	// PartialReasons describe remaining personal reads, not generation failures.
	PartialReasons []string
	// EnvKeys are the references to resolve again when resuming this generation.
	EnvKeys []string
}

// Generate materializes only enabled catalog entries. Both paths must already
// exist as private directories. The caller validates their account scope.
// No HOME or XDG variable is changed. Codex atomically updates accountHome/skills
// to the new generation copy. This shared skill view is not pinned per session.
func (m *ConfigManager) Generate(harness, accountHome, generationRoot string) (ConfigGeneration, error) {
	return m.GenerateWithEnv(harness, accountHome, generationRoot, os.LookupEnv)
}

// GenerateWithEnv resolves environment key references only at materialization.
// A backend can supply a bounded lookup callback without modifying the catalog.
func (m *ConfigManager) GenerateWithEnv(harness, accountHome, generationRoot string, lookup func(string) (string, bool)) (ConfigGeneration, error) {
	return m.GenerateWithPreferences(harness, accountHome, generationRoot, lookup, nil)
}
func (m *ConfigManager) GenerateWithPreferences(harness, accountHome, generationRoot string, lookup func(string) (string, bool), preferences map[string]any) (ConfigGeneration, error) {
	return m.generateWithPreferences(harness, accountHome, generationRoot, lookup, preferences, nil)
}

// GenerateSnapshotAt materializes an already captured catalog in staging, while
// emitted paths name published. It does not publish shared account-home views.
// The caller owns staging, publication, and the generation lifecycle lock.
func (m *ConfigManager) GenerateSnapshotAt(harness, accountHome, staging, published string, catalog HarnessConfig, lookup func(string) (string, bool), preferences map[string]any) (ConfigGeneration, error) {
	if filepath.Dir(staging) != filepath.Dir(published) || staging == published || filepath.Clean(published) != published {
		return ConfigGeneration{}, errors.New("invalid generation publication destination")
	}
	return m.generateWithPreferences(harness, accountHome, filepath.Dir(staging), lookup, preferences, &generationDestination{staging, published, catalog})
}

type generationDestination struct {
	staging, published string
	catalog            HarnessConfig
}

func (m *ConfigManager) generateWithPreferences(harness, accountHome, generationRoot string, lookup func(string) (string, bool), preferences map[string]any, destination *generationDestination) (out ConfigGeneration, err error) {
	hs, err := selectedHarnesses(harness)
	if err != nil || len(hs) != 1 || (hs[0] != "claude-code" && hs[0] != "codex") {
		return out, errors.New("native config generation supports only Claude Code and Codex")
	}
	h := hs[0]
	if err := m.validatePrivateDir(accountHome); err != nil {
		return out, fmt.Errorf("account home: %w", err)
	}
	if err := m.validatePrivateDir(generationRoot); err != nil {
		return out, fmt.Errorf("generation root: %w", err)
	}
	if pathWithin(accountHome, generationRoot) || pathWithin(generationRoot, accountHome) {
		return out, errors.New("generation root and account home must be separate directories")
	}
	var c HarnessConfig
	if destination != nil {
		c = destination.catalog
	} else {
		c, err = m.Read()
		if err != nil {
			return out, errors.New("cannot read Dyna config catalog")
		}
	}
	servers := map[string]any{}
	execSpecs := map[string]MCPExecSpec{}
	envRefs := map[string]string{}
	out.Env = map[string]string{}
	for _, x := range c.MCP {
		if !slices.Contains(x.EnabledHarnesses, h) {
			continue
		}
		if err := validateConfigName(x.Name); err != nil {
			return ConfigGeneration{}, err
		}
		d := x.MCPDefinition
		if err := validateMCPDefinition(d); err != nil {
			return ConfigGeneration{}, err
		}
		native := map[string]any{}
		if d.URL != "" {
			native["url"] = d.URL
			if h == "claude-code" {
				native["type"] = "http"
			}
		} else {
			native["command"] = d.Command
			native["args"] = slices.Clone(d.Args)
			if d.Args == nil {
				native["args"] = []string{}
			}
			env := map[string]string{}
			for _, key := range d.EnvKeys {
				if lookup == nil {
					return ConfigGeneration{}, fmt.Errorf("missing environment key %s", key)
				}
				value, ok := lookup(key)
				if !ok || value == "" {
					return ConfigGeneration{}, fmt.Errorf("missing environment key %s", key)
				}
				env[key] = value
			}
			for key, value := range env {
				alias := MCPEnvironmentName(key)
				out.Env[alias] = value
				envRefs[alias] = key
				if !slices.Contains(out.EnvKeys, key) {
					out.EnvKeys = append(out.EnvKeys, key)
				}
			}
			if h == "claude-code" {
				references := map[string]string{}
				for key := range env {
					references[key] = "${" + MCPEnvironmentName(key) + "}"
				}
				native["env"] = references
			} else {
				names := []string{}
				refs := map[string]string{}
				wrapped := false
				for _, key := range d.EnvKeys {
					alias := MCPEnvironmentName(key)
					names = append(names, alias)
					refs[key] = alias
					wrapped = wrapped || alias != key
				}
				native["env_vars"] = names
				if wrapped {
					cli, err := dynaMCPExecutable(lookup)
					if err != nil {
						return ConfigGeneration{}, err
					}
					native["command"] = cli
					native["args"] = []string{"mcp", "serve", "__DYNA_GENERATION__", x.Name}
					execSpecs[x.Name] = MCPExecSpec{d.Command, d.Args, refs}
				}
			}
		}
		if _, exists := servers[x.Name]; exists {
			return ConfigGeneration{}, errors.New("duplicate enabled MCP name")
		}
		servers[x.Name] = native
	}
	skills := map[string]string{}
	for _, x := range c.Skills {
		if !slices.Contains(x.EnabledHarnesses, h) {
			continue
		}
		if err := validateConfigName(x.Name); err != nil {
			return ConfigGeneration{}, err
		}
		src := x.Source
		if err := validateSkillSource(src); err != nil {
			return ConfigGeneration{}, err
		}
		if _, exists := skills[x.Name]; exists {
			return ConfigGeneration{}, errors.New("duplicate enabled skill name")
		}
		skills[x.Name] = src
	}
	var directory, published string
	if destination == nil {
		directory, err = os.MkdirTemp(generationRoot, "generation-")
		published = directory
	} else {
		directory, published = destination.staging, destination.published
		err = m.validatePrivateDir(directory)
	}
	if err != nil {
		return ConfigGeneration{}, errors.New("cannot create private config generation")
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	out.Directory = published
	out.ID = filepath.Base(published)
	write := func(name string, value any) error {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return errors.New("cannot encode private config generation")
		}
		if err := os.WriteFile(filepath.Join(directory, name), append(b, '\n'), 0600); err != nil {
			return errors.New("cannot write private config generation")
		}
		return nil
	}
	for _, value := range servers {
		if row, ok := value.(map[string]any); ok {
			if args, ok := row["args"].([]string); ok {
				for i, arg := range args {
					if arg == "__DYNA_GENERATION__" {
						args[i] = published
					}
				}
			}
		}
	}
	if len(execSpecs) > 0 {
		if err := write("mcp-exec.json", execSpecs); err != nil {
			return ConfigGeneration{}, err
		}
	}
	if h == "claude-code" {
		if err := write("mcp.json", map[string]any{"mcpServers": servers}); err != nil {
			return ConfigGeneration{}, err
		}
		settings := map[string]any{}
		for k, v := range preferences {
			putPreference(settings, k, v)
		}
		settings["attribution"] = map[string]string{"commit": "", "pr": ""}
		if err := write("settings.json", settings); err != nil {
			return ConfigGeneration{}, err
		}
		out.Env["CLAUDE_CONFIG_DIR"] = accountHome
		// The user source is the private CLAUDE_CONFIG_DIR. It loads the
		// CLAUDE.md that carries Dyna's global rules; --settings still wins.
		out.Args = []string{"--mcp-config", filepath.Join(published, "mcp.json"), "--strict-mcp-config", "--settings", filepath.Join(published, "settings.json"), "--setting-sources", "user"}
		if len(skills) > 0 {
			plugin := filepath.Join(directory, "plugin")
			if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0700); err != nil {
				return ConfigGeneration{}, errors.New("cannot create skill plugin")
			}
			if err := write("plugin/.claude-plugin/plugin.json", map[string]string{"name": "dyna", "version": "1.0.0"}); err != nil {
				return ConfigGeneration{}, err
			}
			for name, src := range skills {
				if err := copyGenerationSkill(src, filepath.Join(plugin, "skills", name)); err != nil {
					return ConfigGeneration{}, err
				}
			}
			out.Args = append(out.Args, "--plugin-dir", filepath.Join(published, "plugin"))
		}
	} else {
		// env_vars keeps secret values out of argv. Override the entire server map,
		// including an empty map, so removed definitions cannot survive from a home.
		preferenceKeys := make([]string, 0, len(preferences))
		for k := range preferences {
			preferenceKeys = append(preferenceKeys, k)
		}
		sort.Strings(preferenceKeys)
		for _, k := range preferenceKeys {
			out.Args = append(out.Args, "-c", k+"="+inlineTOML(preferences[k]))
		}
		out.Args = append(out.Args, []string{"-c", "mcp_servers=" + inlineTOML(servers), "-c", "check_for_update_on_startup=false", "-c", `cli_auth_credentials_store="file"`}...)
		out.Env["CODEX_HOME"] = accountHome
		out.Env["CODEX_SQLITE_HOME"] = filepath.Join(accountHome, "sqlite")
		// Codex runs with a private HOME, so ~/.agents/skills is Dyna's own;
		// only the unpinned skill view remains.
		_, skillReason := NativeSkillCapability("codex")
		out.PartialReasons = []string{skillReason}
		skillDirectory := filepath.Join(directory, "skills")
		if err := os.Mkdir(skillDirectory, 0700); err != nil {
			return ConfigGeneration{}, errors.New("cannot create Codex skill generation")
		}
		for name, src := range skills {
			if err := copyGenerationSkill(src, filepath.Join(skillDirectory, name)); err != nil {
				return ConfigGeneration{}, err
			}
		}
		if err := write("mcp.json", map[string]any{"mcp_servers": servers}); err != nil {
			return ConfigGeneration{}, err
		}
	}
	sort.Strings(out.EnvKeys)
	if err := write("generation.json", generationManifest{Version: 1, Harness: h, AccountHome: accountHome, Args: out.Args, EnvKeys: out.EnvKeys, PartialReasons: out.PartialReasons, EnvReferences: envRefs}); err != nil {
		return ConfigGeneration{}, err
	}
	if h == "codex" && destination == nil {
		if err := PublishSkillView(filepath.Join(accountHome, "skills"), filepath.Join(directory, "skills")); err != nil {
			return ConfigGeneration{}, err
		}
	}
	complete = true
	return out, nil
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (m *ConfigManager) validatePrivateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return errors.New("requires an explicit clean absolute private directory")
	}
	homes := []string{m.Home}
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, home)
	}
	for _, home := range homes {
		if home == "" {
			continue
		}
		if path == home {
			return errors.New("owner HOME is not a private runtime directory")
		}
		for _, personal := range []string{".claude", ".codex", ".agents", ".config"} {
			if pathWithin(filepath.Join(home, personal), path) {
				return errors.New("personal config directories are not runtime directories")
			}
		}
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("private directory must exist without symlink components")
		}
		if p == path && info.Mode().Perm()&0077 != 0 {
			return errors.New("private directory permissions must exclude group and other access")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

// inlineTOML encodes native MCP values and preference scalars as one TOML value.
// JSON string escaping is also valid for these TOML basic strings.
func inlineTOML(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		b, _ := json.Marshal(x)
		return string(b)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = inlineTOML(s)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, inlineTOML(k)+"="+inlineTOML(x[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		panic("unsupported native MCP TOML type")
	}
}
func copyGenerationSkill(source, target string) error {
	// Resolve only the explicitly selected source root. Reject links inside it so
	// a session's immutable copy cannot point back to mutable external content.
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return errors.New("cannot resolve skill source")
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read skill source")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return errors.New("invalid skill source")
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0700); err != nil {
				return errors.New("cannot create skill generation")
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("skill generation requires regular files without symlinks")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("cannot read skill source")
		}
		mode := os.FileMode(0600)
		if info.Mode().Perm()&0100 != 0 {
			mode = 0700
		}
		if err := os.WriteFile(destination, data, mode); err != nil {
			return errors.New("cannot copy skill generation")
		}
		return nil
	})
}

// NativeSkillCapability describes generation support independently of personal
// skill discovery. Claude generation skills use a session-only native plugin.
func NativeSkillCapability(harness string) (supported bool, reason string) {
	hs, err := selectedHarnesses(harness)
	if err != nil || len(hs) != 1 {
		return false, "select one supported harness"
	}
	switch hs[0] {
	case "claude-code":
		return true, ""
	case "codex":
		return true, "Codex uses the shared CODEX_HOME/skills view; Dyna skills are not pinned per generation and change when Dyna generates config"
	default:
		return false, "private skill generation is not implemented for this harness"
	}
}

type generationManifest struct {
	EnvReferences  map[string]string `json:"envReferences,omitempty"`
	Version        int               `json:"version"`
	Harness        string            `json:"harness"`
	AccountHome    string            `json:"accountHome"`
	Args           []string          `json:"args"`
	EnvKeys        []string          `json:"envKeys"`
	PartialReasons []string          `json:"partialReasons,omitempty"`
}

// LoadGeneration restores a launch snapshot without reading today's catalog.
// It resolves key references again and never modifies the retained generation.
// Loading a Codex generation leaves the current shared account skill view unchanged.
func LoadGeneration(directory string) (ConfigGeneration, error) {
	return LoadGenerationWithEnv(directory, os.LookupEnv)
}
func LoadGenerationWithEnv(directory string, lookup func(string) (string, bool)) (ConfigGeneration, error) {
	m := &ConfigManager{}
	if err := m.validatePrivateDir(directory); err != nil {
		return ConfigGeneration{}, err
	}
	path := filepath.Join(directory, "generation.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return ConfigGeneration{}, errors.New("missing or unsafe private generation manifest")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ConfigGeneration{}, errors.New("cannot read private generation manifest")
	}
	var manifest generationManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return ConfigGeneration{}, errors.New("invalid private generation manifest")
	}
	// TODO: legacy/shim remove later — accepts Claude manifests written with the old ID until their generation pins expire.
	manifest.Harness = CanonicalHarnessID(manifest.Harness)
	if manifest.Version != 1 || (manifest.Harness != "claude-code" && manifest.Harness != "codex") {
		return ConfigGeneration{}, errors.New("invalid private generation manifest")
	}
	if err := m.validatePrivateDir(manifest.AccountHome); err != nil {
		return ConfigGeneration{}, errors.New("generation account home is unavailable or unsafe")
	}
	if pathWithin(manifest.AccountHome, directory) || pathWithin(directory, manifest.AccountHome) {
		return ConfigGeneration{}, errors.New("generation and account home overlap")
	}
	g := ConfigGeneration{ID: filepath.Base(directory), Directory: directory, Args: manifest.Args, EnvKeys: manifest.EnvKeys, PartialReasons: manifest.PartialReasons, Env: map[string]string{}}
	for _, key := range manifest.EnvKeys {
		if !configEnvKey.MatchString(key) || reservedRuntimeEnvKey(key) {
			return ConfigGeneration{}, errors.New("invalid generation environment key")
		}
		if lookup == nil {
			return ConfigGeneration{}, fmt.Errorf("missing environment key %s", key)
		}
		value, ok := lookup(key)
		if !ok || value == "" {
			return ConfigGeneration{}, fmt.Errorf("missing environment key %s", key)
		}
		alias := MCPEnvironmentName(key)
		if source, ok := manifest.EnvReferences[alias]; ok && source != key {
			return ConfigGeneration{}, errors.New("invalid environment reference")
		}
		g.Env[alias] = value
	}
	// Check all retained files before returning arguments that reference them.
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("generation files are unavailable")
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe generation file")
		}
		return nil
	}); err != nil {
		return ConfigGeneration{}, err
	}
	if manifest.Harness == "claude-code" {
		g.Env["CLAUDE_CONFIG_DIR"] = manifest.AccountHome
		for _, name := range []string{"mcp.json", "settings.json"} {
			if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
				return ConfigGeneration{}, errors.New("retained generation file is missing")
			}
		}
	} else {
		g.Env["CODEX_HOME"] = manifest.AccountHome
		g.Env["CODEX_SQLITE_HOME"] = filepath.Join(manifest.AccountHome, "sqlite")
	}
	return g, nil
}

// Keep MCP references from replacing native scope or the owner environment.
func reservedRuntimeEnvKey(key string) bool {
	return key == "HOME" || strings.HasPrefix(key, "XDG_") || key == "CODEX_HOME" || key == "CODEX_SQLITE_HOME" || key == "CLAUDE_CONFIG_DIR"
}

func putPreference(root map[string]any, key string, value any) {
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
