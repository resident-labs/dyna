// Package settings loads and saves the per-user dyna settings.json file.
// Settings are hand-edited configuration, so they stay in JSON instead of
// the runtime state database (rewrite requirement 20). Known V1 keys live here.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/store"
)

const (
	Version = 1

	KeyLocalLaneRepos        = "lane.local.repos"
	KeyComputersEnabled      = "computers.enabled"
	KeyAutoCheck             = "updater.autoCheck"
	KeyRunsRetention         = "runs.retention"
	KeyWorktreesRetention    = "worktrees.retention"
	KeyWorktreesRoot         = "worktrees.root"
	KeyWorktreesMinFreeBytes = "worktrees.minFreeBytes"
	KeyRootProfile           = "root.profile"
	KeySubagentMaxDepth      = "subagent.maxDepth"
	KeySteeringToolWait      = "steering.maxToolWait"
	KeyMaxCardBytes          = "content.maxCardBytes"
	KeyMaxFileBytes          = "content.maxFileBytes"
	SourceDefault            = "default"
	SourceFile               = "file"
	SourceEnv                = "env"
	LiveRestart              = "restart"
	LiveNextOpen             = "next-open"
	LiveStored               = "stored"
	LiveNextOperation        = "next-operation"

	WorktreesRetentionChanged = "changed"
	WorktreesRetentionAll     = "all"
	WorktreesRetentionNone    = "none"
)

// Defaults for the subagent and steering keys.
const (
	DefaultSubagentMaxDepth            = 3
	DefaultSteeringToolWait            = 120 * time.Second
	DefaultWorktreesMinFreeBytes int64 = 10 << 30
)

// Values are the effective V1 settings. Unknown file keys are kept in Extra.
type Values struct {
	LocalLaneRepos        []string
	LibraryEnabled        []string
	LibraryProfile        string
	LibraryReadmeMaxLines int
	LocalCIRemote         string
	RootProfile           string
	BrowserPath           string
	BrowserExtraFlags     string
	BrowserUseFlagsFile   bool
	BrowserProfileMode    string
	BrowserOwnerProfile   string
	BrowserSaveBack       bool
	ComputersEnabled      bool
	GoalKeeperTimeout     time.Duration
	MergeStewardProfile   string
	GoalKeeperProfile     string
	MaxCardBytes          int64
	MaxFileBytes          int64
	AutoCheck             bool
	RunsRetention         time.Duration
	WorktreesRetention    string
	WorktreesRoot         string
	WorktreesMinFreeBytes int64
	// SubagentMaxDepth caps subagent-session nesting (top-level sessions are
	// depth 1).
	SubagentMaxDepth int
	// SteeringMaxToolWait bounds how long a steering message or journal
	// reminder waits for an in-flight tool call before interrupting it.
	SteeringMaxToolWait time.Duration
	// VoiceProvider, VoiceModel and VoiceURL choose the call's realtime
	// voice provider.
	VoiceProvider string
	VoiceModel    string
	VoiceURL      string
	Extra         map[string]any
	path          string
	fromFile      map[string]bool
}

func DefaultPath() string {
	return filepath.Join(store.ConfigDir(), "settings.json")
}

func Defaults() Values {
	return Values{
		LocalCIRemote:         DefaultLocalCIRemote,
		RootProfile:           "opus",
		BrowserUseFlagsFile:   true,
		BrowserProfileMode:    "shared",
		BrowserOwnerProfile:   "Default",
		BrowserSaveBack:       true,
		ComputersEnabled:      true,
		GoalKeeperTimeout:     5 * time.Minute,
		LibraryReadmeMaxLines: DefaultLibraryReadmeMaxLines,
		MaxCardBytes:          DefaultContentBytes,
		MaxFileBytes:          DefaultContentBytes,
		AutoCheck:             true,
		RunsRetention:         14 * 24 * time.Hour,
		WorktreesRetention:    WorktreesRetentionChanged,
		WorktreesRoot:         "~/.dyna-worktrees",
		WorktreesMinFreeBytes: DefaultWorktreesMinFreeBytes,
		SubagentMaxDepth:      DefaultSubagentMaxDepth,
		SteeringMaxToolWait:   DefaultSteeringToolWait,
		VoiceProvider:         "openai",
		Extra:                 map[string]any{},
		fromFile:              map[string]bool{},
	}
}

func Load(path string) (Values, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	v := Defaults()
	v.path = path
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return v, nil
		}
		return Values{}, fmt.Errorf("read settings: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	// A JSON number stays exact text, so an int64 setting such as a byte
	// limit is not rounded through float64 before it is read.
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return Values{}, fmt.Errorf("parse settings: %w", err)
	}
	// A decoder takes the first value only. Keep the old strictness, so a
	// second object or a suffix is refused instead of dropped by the next
	// save.
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("more than one JSON value")
		}
		return Values{}, fmt.Errorf("parse settings: %w", err)
	}
	if rawVersion, present := raw["version"]; present {
		ver, ok := asInt(rawVersion)
		if !ok {
			return Values{}, errors.New("settings version must be an integer")
		}
		if ver != Version {
			return Values{}, fmt.Errorf("unsupported settings version %d", ver)
		}
	}
	v.Extra = raw
	if localci, ok := raw["localci"].(map[string]any); ok {
		if remote, exists := localci["remote"]; exists {
			value, ok := remote.(string)
			if !ok {
				return Values{}, fmt.Errorf("localci.remote must be a string")
			}
			if err := v.Set(KeyLocalCIRemote, value); err != nil {
				return Values{}, err
			}
		}
	}
	if computers, ok := raw["computers"].(map[string]any); ok {
		if enabled, ok := computers["enabled"].(bool); ok {
			v.ComputersEnabled = enabled
			v.fromFile[KeyComputersEnabled] = true
		}
	}
	if section, exists := raw["lane"]; exists {
		lane, ok := section.(map[string]any)
		if !ok {
			return Values{}, fmt.Errorf("lane settings must be an object")
		}
		if section, exists := lane["local"]; exists {
			local, ok := section.(map[string]any)
			if !ok {
				return Values{}, fmt.Errorf("lane.local settings must be an object")
			}
			if value, exists := local["repos"]; exists {
				data, err := json.Marshal(value)
				if err != nil {
					return Values{}, err
				}
				if err := v.Set(KeyLocalLaneRepos, string(data)); err != nil {
					return Values{}, err
				}
			}
		}
	}
	if err := v.loadBrowser(raw); err != nil {
		return Values{}, err
	}
	if err := v.loadMerge(raw); err != nil {
		return Values{}, err
	}
	if err := v.loadGoals(raw); err != nil {
		return Values{}, err
	}
	if err := v.loadVoice(raw); err != nil {
		return Values{}, err
	}
	if err := v.loadLibrary(raw); err != nil {
		return Values{}, err
	}
	if updater, ok := raw["updater"].(map[string]any); ok {
		if auto, ok := updater["autoCheck"].(bool); ok {
			v.AutoCheck = auto
			v.fromFile[KeyAutoCheck] = true
		}
	}
	if runs, ok := raw["runs"].(map[string]any); ok {
		if value, exists := runs["retention"]; exists {
			d, err := parseRunsRetention(numberText(value))
			if err != nil {
				return Values{}, err
			}
			v.RunsRetention = d
			v.fromFile[KeyRunsRetention] = true
		}
	}
	if worktrees, ok := raw["worktrees"].(map[string]any); ok {
		if rawMin, exists := worktrees["minFreeBytes"]; exists {
			n, ok := asInt(rawMin)
			if !ok || n < 0 {
				return Values{}, fmt.Errorf("worktrees.minFreeBytes must be a non-negative integer number of bytes")
			}
			v.WorktreesMinFreeBytes = int64(n)
			v.fromFile[KeyWorktreesMinFreeBytes] = true
		}
		if root, exists := worktrees["root"]; exists {
			value, ok := root.(string)
			if !ok || !validWorktreesRoot(value) {
				return Values{}, fmt.Errorf("worktrees.root must be an absolute path or start with ~/")
			}
			v.WorktreesRoot = strings.TrimSpace(value)
			v.fromFile[KeyWorktreesRoot] = true
		}
		if retention, ok := worktrees["retention"].(string); ok {
			if ValidWorktreesRetention(retention) {
				v.WorktreesRetention = retention
				v.fromFile[KeyWorktreesRetention] = true
			}
		}
	}
	if section, exists := raw["root"]; exists {
		root, ok := section.(map[string]any)
		if !ok {
			return Values{}, fmt.Errorf("root settings must be an object")
		}
		if value, exists := root["profile"]; exists {
			name, ok := value.(string)
			if !ok {
				return Values{}, fmt.Errorf("root.profile must be a string")
			}
			if err := v.Set(KeyRootProfile, name); err != nil {
				return Values{}, err
			}
		}
	}
	if sub, ok := raw["subagent"].(map[string]any); ok {
		if depth, ok := asInt(sub["maxDepth"]); ok && depth >= 1 {
			v.SubagentMaxDepth = depth
			v.fromFile[KeySubagentMaxDepth] = true
		}
	}
	if steer, ok := raw["steering"].(map[string]any); ok {
		if wait, err := parseToolWait(fmt.Sprint(steer["maxToolWait"])); steer["maxToolWait"] != nil && err == nil {
			v.SteeringMaxToolWait = wait
			v.fromFile[KeySteeringToolWait] = true
		}
	}
	if section, exists := raw["content"]; exists {
		content, ok := section.(map[string]any)
		if !ok {
			return Values{}, fmt.Errorf("content settings must be an object with maxCardBytes and maxFileBytes integer byte limits")
		}
		for key, field := range map[string]*int64{KeyMaxCardBytes: &v.MaxCardBytes, KeyMaxFileBytes: &v.MaxFileBytes} {
			if value, ok := content[strings.TrimPrefix(key, "content.")]; ok {
				n, valid := asInt(value)
				if !valid {
					return Values{}, fmt.Errorf("%s must be an integer number of bytes", key)
				}
				parsed, err := parseContentBytes(key, strconv.Itoa(n))
				if err != nil {
					return Values{}, err
				}
				*field = parsed
				v.fromFile[key] = true
			}
		}
	}
	return v, nil
}

// parseToolWait accepts a Go duration ("90s", "2m") or bare seconds ("120").
func parseToolWait(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	var d time.Duration
	if secs, err := strconv.ParseFloat(value, 64); err == nil {
		d = time.Duration(secs * float64(time.Second))
	} else if parsed, err := time.ParseDuration(value); err == nil {
		d = parsed
	} else {
		return 0, fmt.Errorf("steering.maxToolWait must be a duration like 120s or 2m")
	}
	if d <= 0 {
		return 0, fmt.Errorf("steering.maxToolWait must be positive")
	}
	return d, nil
}

func (v Values) Path() string {
	if v.path != "" {
		return v.path
	}
	return DefaultPath()
}

func (v Values) Source(key string) string {
	if key == KeyAutoCheck && os.Getenv("DYNA_NO_AUTO_UPDATE") == "1" {
		return SourceEnv
	}
	if v.fromFile[key] {
		return SourceFile
	}
	return SourceDefault
}

func (v Values) EffectiveAutoCheck() bool {
	if os.Getenv("DYNA_NO_AUTO_UPDATE") == "1" {
		return false
	}
	return v.AutoCheck
}

func (v *Values) Set(key, value string) error {
	switch key {
	case KeyLocalLaneRepos:
		var repos []string
		if err := json.Unmarshal([]byte(value), &repos); err != nil {
			return fmt.Errorf("lane.local.repos must be a JSON list of absolute repository paths")
		}
		for _, repo := range repos {
			if !filepath.IsAbs(repo) || strings.ContainsRune(repo, 0) {
				return fmt.Errorf("lane.local.repos requires absolute repository paths")
			}
		}
		v.LocalLaneRepos = repos
		v.fromFile[key] = true
	case KeyLocalCIRemote:
		if _, _, err := ParseLocalCIRemote(value); err != nil {
			return err
		}
		v.LocalCIRemote = value
		v.fromFile[key] = true
	case KeyBrowserPath, KeyBrowserExtraFlags, KeyBrowserUseFlagsFile, KeyBrowserProfileMode, KeyBrowserOwnerProfile, KeyBrowserSaveBack:
		return v.setBrowser(key, value)
	case KeyComputersEnabled:
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		v.ComputersEnabled = b
		v.fromFile[key] = true
	case KeyMergeStewardProfile:
		value = strings.TrimSpace(value)
		if strings.EqualFold(value, StewardProfileOff) {
			value = StewardProfileOff
		}
		v.MergeStewardProfile = value
		v.fromFile[key] = true
	case KeyGoalKeeperTimeout, KeyGoalKeeperProfile:
		return v.setGoal(key, value)
	case KeyVoiceProvider, KeyVoiceModel, KeyVoiceURL:
		return v.setVoice(key, value)
	case KeyLibraryEnabled, KeyLibraryProfile, KeyLibraryReadmeMaxLines:
		return v.setLibrary(key, value)
	case KeyMaxCardBytes, KeyMaxFileBytes:
		n, err := parseContentBytes(key, value)
		if err != nil {
			return err
		}
		if key == KeyMaxCardBytes {
			v.MaxCardBytes = n
		} else {
			v.MaxFileBytes = n
		}
		v.fromFile[key] = true
	case KeyAutoCheck:
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		v.AutoCheck = b
		v.fromFile[key] = true
	case KeyRunsRetention:
		d, err := parseRunsRetention(value)
		if err != nil {
			return err
		}
		v.RunsRetention = d
		v.fromFile[key] = true
	case KeyWorktreesRetention:
		value = strings.ToLower(strings.TrimSpace(value))
		if !ValidWorktreesRetention(value) {
			return fmt.Errorf("worktrees.retention must be one of changed, all, none")
		}
		v.WorktreesRetention = value
		v.fromFile[key] = true
	case KeyWorktreesRoot:
		value = strings.TrimSpace(value)
		if !validWorktreesRoot(value) {
			return fmt.Errorf("worktrees.root must be an absolute path or start with ~/")
		}
		v.WorktreesRoot = value
		v.fromFile[key] = true
	case KeyWorktreesMinFreeBytes:
		var n int64
		var err error
		if strings.TrimSpace(value) != "0" {
			n, err = ParseBytes(value)
		}
		if err != nil {
			return fmt.Errorf("worktrees.minFreeBytes: %w", err)
		}
		v.WorktreesMinFreeBytes = n
		v.fromFile[key] = true
	case KeyRootProfile:
		v.RootProfile = strings.TrimSpace(value)
		v.fromFile[key] = true
	case KeySubagentMaxDepth:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 {
			return fmt.Errorf("subagent.maxDepth must be an integer of at least 1")
		}
		v.SubagentMaxDepth = n
		v.fromFile[key] = true
	case KeySteeringToolWait:
		d, err := parseToolWait(value)
		if err != nil {
			return err
		}
		v.SteeringMaxToolWait = d
		v.fromFile[key] = true
	default:
		return fmt.Errorf("unknown settings key %q", key)
	}
	return nil
}

func (v Values) Save() error {
	path := v.Path()
	raw := v.Extra
	if raw == nil {
		raw = map[string]any{}
	}
	raw["version"] = Version
	lane := nested(raw, "lane")
	local := nested(lane, "local")
	local["repos"] = v.LocalLaneRepos
	lane["local"] = local
	raw["lane"] = lane
	localci := nested(raw, "localci")
	localci["remote"] = v.LocalCIRemote
	raw["localci"] = localci
	computers := nested(raw, "computers")
	computers["enabled"] = v.ComputersEnabled
	raw["computers"] = computers
	v.saveBrowser(raw)
	merge := nested(raw, "merge")
	merge["stewardProfile"] = v.MergeStewardProfile
	raw["merge"] = merge
	v.saveGoals(raw)
	v.saveVoice(raw)
	v.saveLibrary(raw)
	content := nested(raw, "content")
	content["maxCardBytes"] = v.MaxCardBytes
	content["maxFileBytes"] = v.MaxFileBytes
	raw["content"] = content
	updater := nested(raw, "updater")
	updater["autoCheck"] = v.AutoCheck
	raw["updater"] = updater
	runs := nested(raw, "runs")
	runs["retention"] = formatRunsRetention(v.RunsRetention)
	raw["runs"] = runs
	worktrees := nested(raw, "worktrees")
	worktrees["retention"] = v.WorktreesRetention
	worktrees["root"] = v.WorktreesRoot
	worktrees["minFreeBytes"] = v.WorktreesMinFreeBytes
	raw["worktrees"] = worktrees
	root := nested(raw, "root")
	root["profile"] = v.RootProfile
	raw["root"] = root
	sub := nested(raw, "subagent")
	sub["maxDepth"] = v.SubagentMaxDepth
	raw["subagent"] = sub
	steer := nested(raw, "steering")
	steer["maxToolWait"] = v.SteeringMaxToolWait.String()
	raw["steering"] = steer
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	return writeAtomic(path, append(b, '\n'), 0o644)
}

// ValidWorktreesRetention reports whether value is an engine retention policy.
func ValidWorktreesRetention(value string) bool {
	switch value {
	case WorktreesRetentionChanged, WorktreesRetentionAll, WorktreesRetentionNone:
		return true
	default:
		return false
	}
}

func nested(raw map[string]any, key string) map[string]any {
	if m, ok := raw[key].(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("boolean value must be true or false")
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), float64(int(n)) == n
	case int:
		return n, true
	case json.Number:
		return numberAsInt(n)
	default:
		return 0, false
	}
}

// numberText renders a JSON value as the digits of an integral number, so a
// known integer setting accepts 3e2 and 300.0 as it did before numbers stayed
// exact. Anything else keeps its own text for the existing validation.
func numberText(v any) string {
	if n, ok := v.(json.Number); ok {
		if i, ok := numberAsInt(n); ok {
			return strconv.Itoa(i)
		}
	}
	return fmt.Sprint(v)
}

// numberAsInt reads a JSON number that is mathematically integral: 4, 4.0
// and 1e9 all name an integer. The value is exact, so a fractional number or
// one that does not fit an int is not an int.
func numberAsInt(n json.Number) (int, bool) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	i := r.Num().Int64()
	if int64(int(i)) != i {
		return 0, false
	}
	return int(i), true
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return fsutil.WriteFile(path, data, mode, true)
}

func validWorktreesRoot(value string) bool {
	return filepath.IsAbs(strings.TrimSpace(value)) || strings.HasPrefix(strings.TrimSpace(value), "~/")
}

// parseRunsRetention accepts Go durations and whole days. Zero or off disables cleanup.
func parseRunsRetention(value string) (time.Duration, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "off" || value == "0" {
		return 0, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 64)
		if err == nil && days >= 0 && days <= int64((1<<63-1)/(24*time.Hour)) {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	} else if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return d, nil
	}
	return 0, fmt.Errorf("runs.retention must be a non-negative duration such as 14d or 24h, or off")
}

func formatRunsRetention(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
}
