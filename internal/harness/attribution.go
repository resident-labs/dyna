package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dyna must leave no visible traces in the user's repositories, so every
// harness launch turns commit and pull request attribution off per launch,
// with flags or settings only. Dyna never edits the owner's global harness
// config files.
//
// What each harness supports:
//   - Claude Code accepts a --settings JSON layer. The attribution object sets
//     the commit trailer and the PR footer to empty and disables commit
//     trailers and the session URL.

// claudeAttributionSettings is the --settings layer that hides Claude Code's
// commit and pull request attribution. An empty string hides that attribution;
// the object form is used because older builds reject "attribution": false.
const claudeAttributionSettings = `{"attribution":{"commit":"","pr":"","sessionUrl":false,"commitTrailers":false}}`

// claudeArgsWithoutAttribution returns args with one final --settings layer
// that turns attribution off. A profile --settings file or JSON object merges
// into that layer. The merge is shallow, so a profile's own attribution object
// is replaced as a whole and the attribution keys win. Settings Dyna cannot
// read fail the launch instead of silently keeping attribution on.
func claudeArgsWithoutAttribution(args []string, cwd string) ([]string, error) {
	var off map[string]any
	if err := json.Unmarshal([]byte(claudeAttributionSettings), &off); err != nil {
		return nil, err
	}
	return claudeArgsWithSettings(args, cwd, func(merged map[string]any) {
		for key, value := range off {
			merged[key] = value
		}
	})
}

// claudeArgsWithSettings merges profile settings and applies one launch setting layer.
func claudeArgsWithSettings(args []string, cwd string, apply func(map[string]any)) ([]string, error) {
	out := make([]string, 0, len(args)+2)
	merged := map[string]any{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value, settings := "", false
		switch {
		case arg == "--settings" && i+1 < len(args):
			value, settings = args[i+1], true
			i++
		case strings.HasPrefix(arg, "--settings="):
			value, settings = strings.TrimPrefix(arg, "--settings="), true
		}
		if !settings {
			out = append(out, arg)
			continue
		}
		raw := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			// A settings path resolves against the harness working directory.
			path := value
			if !filepath.IsAbs(path) && cwd != "" {
				path = filepath.Join(cwd, path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("profile --settings %s must be readable so Dyna can apply launch settings: %w", value, err)
			}
			raw = b
		}
		var layer map[string]any
		if err := json.Unmarshal(raw, &layer); err != nil {
			return nil, fmt.Errorf("profile --settings %s must be a JSON object so Dyna can apply launch settings: %w", value, err)
		}
		for key, v := range layer {
			merged[key] = v
		}
	}
	apply(merged)
	b, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return append(out, "--settings", string(b)), nil
}
