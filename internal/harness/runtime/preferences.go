package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"

	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/store"
	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
)

type PreferenceKey struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}
type PreferencePreview struct {
	AccountID string          `json:"accountId"`
	Harness   string          `json:"harness"`
	Hash      string          `json:"hash"`
	Keys      []PreferenceKey `json:"keys"`
}

var errPreferences = errors.New("Preference settings could not be read or saved safely.")

func preferenceAllowlist(a RuntimeAdapter) []string {
	if a.ConfigAllowlist != nil {
		return a.ConfigAllowlist
	}
	switch a.Harness {
	case "claude-code":
		return []string{"language", "spinnerTipsEnabled", "showTurnDuration", "terminalProgressBarEnabled", "prefersReducedMotion"}
	}
	return nil
}

// preferenceDenied remains authoritative even if an adapter's allowlist grows.
func preferenceDenied(key string) bool {
	k := strings.ToLower(key)
	for _, term := range []string{"mcp", "skill", "plugin", "hook", "helper", "command", "env", "credential", "secret", "token", "password", "apikey", "api_key", "auth", "model", "provider", "permission", "approval", "sandbox", "statusline", "status_line", "notification", "notify", "path", "directory", "homedir", "home_dir", "endpoint", "baseurl", "base_url", "instruction", "prompt", "system", "executable", "shell", "tool", "agent", "proxy", "telemetry", "network", "url", "remote", "startup", "update", "trust", "keybinding", "key_binding"} {
		// These two documented display booleans do not control agent execution.
		if k == "hide_agent_reasoning" || k == "show_raw_agent_reasoning" {
			return false
		}
		if strings.Contains(k, term) {
			return true
		}
	}
	return false
}
func preferenceStringSafe(s string) bool {
	if len(s) > 256 || strings.TrimSpace(s) != s {
		return false
	}
	lower := strings.ToLower(s)
	for _, prefix := range []string{"bash ", "sh ", "zsh ", "fish ", "cmd ", "powershell ", "pwsh ", "python ", "python3 ", "node ", "npx ", "npm ", "curl ", "wget ", "exec ", "eval ", "env ", "sudo ", "echo ", "printf ", "rm ", "cp ", "mv ", "chmod ", "chown ", "touch ", "sed ", "awk ", "grep ", "find ", "git "} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if strings.Contains(s, " --") || strings.HasPrefix(s, "!") {
		return false
	}
	if len(s) >= 24 && !strings.ContainsAny(s, " ") {
		letters, digits := false, false
		for _, c := range s {
			letters = letters || unicode.IsLetter(c)
			digits = digits || unicode.IsDigit(c)
		}
		if letters && digits {
			return false
		}
	}
	for _, marker := range []string{"sk-", "sk_", "ghp_", "github_pat_", "bearer ", "eyj", "secret", "token", "password", "credential", "api_key", "apikey", "://", "~/", "~\\", "$", "`", ";", "|", "&", ">", "<", "\\", "/"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "~") || strings.Contains(s, ":") {
		return false
	}
	if u, err := url.Parse(s); err == nil && u.User != nil {
		return false
	}
	// Long opaque strings are more likely credentials than personal preferences.
	run := 0
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '-' {
			run++
			if run > 24 {
				return false
			}
		} else {
			run = 0
		}
	}
	return true
}
func preferenceValueSafe(h, key string, value any) bool {
	if preferenceDenied(key) {
		return false
	}
	switch v := value.(type) {
	case string:
		if !preferenceStringSafe(v) {
			return false
		}
	case bool:
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return false
		}
	case int64:
	default:
		return false
	}
	if h == "claude-code" {
		switch key {
		case "language":
			_, ok := value.(string)
			return ok
		case "spinnerTipsEnabled", "showTurnDuration", "terminalProgressBarEnabled", "prefersReducedMotion":
			_, ok := value.(bool)
			return ok
		}
	}
	return true
}
func flattenPreferences(root map[string]any) (map[string]any, error) {
	out := map[string]any{}
	var walk func(map[string]any, string, int) error
	walk = func(m map[string]any, prefix string, depth int) error {
		if depth > 64 {
			return errPreferences
		}
		for k, v := range m {
			if k == "" {
				return errPreferences
			}
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if child, ok := v.(map[string]any); ok && len(child) > 0 {
				if err := walk(child, key, depth+1); err != nil {
					return err
				}
			} else {
				if _, exists := out[key]; exists {
					return errPreferences
				}
				out[key] = v
			}
		}
		return nil
	}
	return out, walk(root, "", 0)
}
func (r *Runtime) preferenceSource(accountID string) (PreferencePreview, map[string]any, error) {
	p := PreferencePreview{AccountID: accountID, Keys: []PreferenceKey{}}
	account, err := r.Accounts.Find(accountID)
	if err != nil {
		return p, nil, errPreferences
	}
	a, err := Adapter(account.Harness)
	if err != nil {
		return p, nil, errPreferences
	}
	p.Harness = a.Harness
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return p, nil, errPreferences
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return p, nil, errPreferences
	}
	personalRoot, err := os.OpenRoot(home)
	if err != nil {
		return p, nil, errPreferences
	}
	defer personalRoot.Close()
	files := a.PersonalConfigFiles
	if files == nil {
		switch a.Harness {
		case "claude-code":
			files = []string{".claude/settings.json"}
		}
	}
	allowed := map[string]bool{}
	names := append([]string{}, preferenceAllowlist(a)...)
	sort.Strings(names)
	for _, name := range names {
		allowed[name] = true
	}
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	_ = encoder.Encode([]string{accountID, a.Harness})
	_ = encoder.Encode(names)
	flat := map[string]any{}
	for _, name := range files {
		if filepath.IsAbs(name) || name == "." || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return p, nil, errPreferences
		}
		path := filepath.Join(home, name)
		if safeAccountPath(path) != nil {
			return p, nil, errPreferences
		}
		f, err := personalRoot.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if os.IsNotExist(err) {
			_ = encoder.Encode([]string{name, "missing"})
			continue
		}
		if err != nil {
			return p, nil, errPreferences
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			return p, nil, errPreferences
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
		_ = f.Close()
		if readErr != nil || len(raw) > 4*1024*1024 {
			return p, nil, errPreferences
		}
		_ = encoder.Encode(name)
		sum := sha256.Sum256(raw)
		_ = encoder.Encode(hex.EncodeToString(sum[:]))
		var root map[string]any
		switch strings.ToLower(filepath.Ext(name)) {
		case ".toml":
			err = toml.Unmarshal(raw, &root)
		case ".json", ".jsonc":
			raw, err = hujson.Standardize(raw)
			if err == nil {
				err = json.Unmarshal(raw, &root)
			}
		default:
			return p, nil, errPreferences
		}
		if err != nil || root == nil {
			return p, nil, errPreferences
		}
		values, err := flattenPreferences(root)
		if err != nil {
			return p, nil, errPreferences
		}
		for k, v := range values {
			if _, exists := flat[k]; exists {
				return p, nil, errPreferences
			}
			flat[k] = v
		}
	}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	accepted := map[string]any{}
	for _, k := range keys {
		status := "not imported (unknown)"
		if preferenceDenied(k) {
			status = "not imported (unsafe)"
		} else if allowed[k] {
			if preferenceValueSafe(a.Harness, k, flat[k]) {
				status = "allowed"
				accepted[k] = flat[k]
			} else {
				status = "not imported (unsafe)"
			}
		}
		p.Keys = append(p.Keys, PreferenceKey{Key: k, Status: status})
	}
	p.Hash = hex.EncodeToString(digest.Sum(nil))
	return p, accepted, nil
}
func (r *Runtime) PreviewPreferenceImport(accountID string) (PreferencePreview, error) {
	p, _, err := r.preferenceSource(accountID)
	if err == nil && !preferenceLayerSupported(p.Harness) {
		for i := range p.Keys {
			if p.Keys[i].Status == "allowed" {
				p.Keys[i].Status = "not imported (no verified settings layer)"
			}
		}
	}
	return p, err
}
func (r *Runtime) preferenceRoot() (string, error) {
	if !filepath.IsAbs(r.Generations) {
		return "", errPreferences
	}
	root := filepath.Join(r.Generations, "preferences")
	if safeAccountPath(root) != nil {
		return "", errPreferences
	}
	return root, nil
}
func (r *Runtime) ImportPreferences(accountID, previewHash string) error {
	p, values, err := r.preferenceSource(accountID)
	if err != nil {
		return err
	}
	if !preferenceLayerSupported(p.Harness) {
		return errors.New("This harness has no verified immutable preference settings layer; preview only.")
	}
	if previewHash == "" || p.Hash != previewHash {
		return errors.New("Personal settings changed. Preview preference import again.")
	}
	if len(values) == 0 {
		return errors.New("No safe personal preferences were accepted.")
	}
	root, err := r.preferenceRoot()
	if err != nil {
		return err
	}
	if os.MkdirAll(root, 0700) != nil {
		return errPreferences
	}
	if os.Chmod(root, 0700) != nil {
		return errPreferences
	}
	dir, err := os.MkdirTemp(root, "layer-")
	if err != nil {
		return errPreferences
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "settings.json")
	if store.WriteJSON(path, values, 0600) != nil {
		return errPreferences
	}
	if preferences.Save(accountID, preferences.Ref{Settings: path, SourceHash: p.Hash, Harness: p.Harness}) != nil {
		return errPreferences
	}
	committed = true
	return nil
}

// PreferenceValues returns flattened dotted keys for the generation builder.
// The builder expands these keys before applying Dyna-controlled configuration.
func (r *Runtime) PreferenceValues(accountID string) (map[string]any, error) {
	out := map[string]any{}
	account, err := r.Accounts.Find(accountID)
	if err != nil {
		return nil, errPreferences
	}
	a, err := Adapter(account.Harness)
	if err != nil {
		return nil, errPreferences
	}
	root, err := r.preferenceRoot()
	if err != nil {
		return nil, err
	}
	ref, ok, err := preferences.For(accountID)
	if err != nil {
		return nil, errPreferences
	}
	if !ok {
		return out, nil
	}
	rel, err := filepath.Rel(root, ref.Settings)
	if err != nil || ref.Harness != a.Harness || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || filepath.Base(ref.Settings) != "settings.json" || filepath.Dir(filepath.Dir(ref.Settings)) != root || safeAccountPath(ref.Settings) != nil {
		return nil, errPreferences
	}
	f, err := os.OpenFile(ref.Settings, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errPreferences
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errPreferences
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return nil, errPreferences
	}
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, errPreferences
	}
	allowed := map[string]bool{}
	for _, k := range preferenceAllowlist(a) {
		allowed[k] = true
	}
	for k, v := range out {
		if !allowed[k] || !preferenceValueSafe(a.Harness, k, v) {
			return nil, errPreferences
		}
	}
	return out, nil
}

func preferenceLayerSupported(h string) bool {
	switch canonicalRuntimeID(h) {
	case "claude-code", "opencode", "muse":
		return true
	}
	return false
}
