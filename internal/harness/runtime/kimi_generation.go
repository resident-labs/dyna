package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"dyna-agent/internal/fsutil"
	"github.com/pelletier/go-toml/v2"
)

// publishKimiGeneration merges only tools.disabled into the account's native
// config. The ACP catalog is passed separately; config.toml is never replaced
// by a generation's settings.json.
func publishKimiGeneration(dir, home string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return err
	}
	var generated struct {
		Tools struct {
			Disabled []string `json:"disabled"`
		} `json:"tools"`
	}
	if json.Unmarshal(raw, &generated) != nil {
		return errors.New("Kimi Code generation settings are invalid")
	}
	if len(generated.Tools.Disabled) == 0 {
		return nil
	}
	configPath := filepath.Join(home, "config.toml")
	if err := checkPrivateAccountFile(configPath, false); err != nil {
		return err
	}
	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Kimi's config is extensible. Unknown fields must survive a policy merge,
	// so no closed Go struct can represent this document safely.
	document := map[string]any{}
	if len(existing) > 0 {
		if err := toml.Unmarshal(existing, &document); err != nil {
			return errors.New("Kimi Code native configuration is invalid")
		}
	}
	tools, ok := document["tools"].(map[string]any)
	if !ok {
		if document["tools"] != nil {
			return errors.New("Kimi Code tools configuration is invalid")
		}
		tools = map[string]any{}
		document["tools"] = tools
	}
	disabled, err := kimiConfigStrings(tools["disabled"])
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range disabled {
		seen[name] = true
	}
	for _, name := range generated.Tools.Disabled {
		if !seen[name] {
			disabled = append(disabled, name)
			seen[name] = true
		}
	}
	tools["disabled"] = disabled
	merged, err := toml.Marshal(document)
	if err != nil {
		return errors.New("Kimi Code native configuration could not be encoded")
	}
	return fsutil.WriteFile(configPath, merged, 0600, false)
}

func kimiConfigStrings(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	if values, ok := value.([]string); ok {
		return append([]string(nil), values...), nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("Kimi Code tools.disabled must be a string list")
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("Kimi Code tools.disabled must be a string list")
		}
		out = append(out, text)
	}
	return out, nil
}
