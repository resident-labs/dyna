package folder

import (
	"encoding/json"
	"fmt"
)

type liveAutoModRule struct {
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

func snapshotAutoMod(out Snapshot, rules []json.RawMessage, byID map[string]string) (Snapshot, error) {
	for _, raw := range rules {
		var rule liveAutoModRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			return out, err
		}
		config, reason := representAutoMod(rule, byID)
		if reason != "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("AutoMod rule %q (ID %s) stays on Discord and is not adopted: %s.", rule.Name, rule.ID, reason))
			continue
		}
		out.Server.AutoMod = append(out.Server.AutoMod, config)
	}
	return out, nil
}

func representAutoMod(rule liveAutoModRule, byID map[string]string) (AutoMod, string) {
	config := AutoMod{Identity: Identity{ID: rule.ID}, Name: rule.Name, Keywords: rule.Metadata.Keywords}
	switch {
	case !rule.Enabled:
		return config, "disabled rules are not represented"
	case rule.Event != 1:
		return config, fmt.Sprintf("event type %d is not represented", rule.Event)
	case rule.Trigger != 1:
		return config, fmt.Sprintf("trigger type %d is not represented", rule.Trigger)
	case len(rule.Metadata.Regex) > 0:
		return config, "regular expressions are not represented"
	case len(rule.Metadata.Allowed) > 0:
		return config, "allow lists are not represented"
	case len(rule.Roles)+len(rule.Channels) > 0:
		return config, "role or channel exemptions are not represented"
	}
	for _, action := range rule.Actions {
		switch action.Type {
		case 1:
			if action.Metadata.Custom != "" {
				return config, "custom block messages are not represented"
			}
			if config.Block {
				return config, "duplicate block actions are not represented"
			}
			config.Block = true
		case 2:
			if config.Alert != "" {
				return config, "duplicate alert actions are not represented"
			}
			config.Alert = byID[action.Metadata.Channel]
			if config.Alert == "" {
				return config, "the alert channel cannot be resolved"
			}
		default:
			return config, fmt.Sprintf("action type %d is not represented", action.Type)
		}
	}
	return config, ""
}
