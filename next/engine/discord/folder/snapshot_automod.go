package folder

import (
	"encoding/json"
	"fmt"
)

func snapshotAutoMod(out Snapshot, rules []json.RawMessage, byID map[string]string) (Snapshot, error) {
	for _, raw := range rules {
		var rule struct {
			ID, Name string
			Event    int `json:"event_type"`
			Trigger  int `json:"trigger_type"`
			Enabled  bool
			Metadata struct {
				Keywords []string `json:"keyword_filter"`
				Regex    []string `json:"regex_patterns"`
				Allowed  []string `json:"allow_list"`
			} `json:"trigger_metadata"`
			Roles    []string `json:"exempt_roles"`
			Channels []string `json:"exempt_channels"`
			Actions  []struct {
				Type     int
				Metadata struct {
					Channel string `json:"channel_id"`
					Custom  string `json:"custom_message"`
				}
			}
		}
		if err := json.Unmarshal(raw, &rule); err != nil {
			return out, err
		}
		if !rule.Enabled || rule.Event != 1 || rule.Trigger != 1 || len(rule.Metadata.Regex)+len(rule.Metadata.Allowed)+len(rule.Roles)+len(rule.Channels) > 0 {
			return out, fmt.Errorf("automod %q cannot be represented", rule.Name)
		}
		config := AutoMod{Identity: Identity{ID: rule.ID}, Name: rule.Name, Keywords: rule.Metadata.Keywords}
		for _, action := range rule.Actions {
			switch action.Type {
			case 1:
				if action.Metadata.Custom != "" {
					return out, fmt.Errorf("custom automod message unsupported")
				}
				config.Block = true
			case 2:
				config.Alert = byID[action.Metadata.Channel]
				if config.Alert == "" {
					return out, fmt.Errorf("unknown alert channel")
				}
			default:
				return out, fmt.Errorf("automod action unsupported")
			}
		}
		out.Server.AutoMod = append(out.Server.AutoMod, config)
	}

	return out, nil
}
