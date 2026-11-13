package folder

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Decode accepts one mapping, without YAML aliases, duplicate keys or unknown fields.
// File names and source lines are retained in every syntax diagnostic.
func Decode(file string, data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: line 1: expected a mapping", file)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s: expected one YAML document", file)
	}
	if err := inspect(&root); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

func inspect(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("line %d: aliases and anchors are refused", node.Line)
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("line %d: mapping keys must be strings", key.Line)
			}
			if seen[key.Value] {
				return fmt.Errorf("line %d: duplicate key %s", key.Line, key.Value)
			}
			seen[key.Value] = true
			switch key.Value {
			case "permissions":
				if err := inspectPermissions(value); err != nil {
					return err
				}
			case "allow", "deny":
				if value.Kind == yaml.MappingNode {
					for j := 1; j < len(value.Content); j += 2 {
						if err := inspectPermissions(value.Content[j]); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	for _, child := range node.Content {
		if err := inspect(child); err != nil {
			return err
		}
	}
	return nil
}

func inspectPermissions(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: permissions must be a list", node.Line)
	}
	for _, permission := range node.Content {
		if _, ok := PermissionBits[permission.Value]; !ok {
			return fmt.Errorf("line %d: unknown permission %s", permission.Line, permission.Value)
		}
	}
	return nil
}

func readYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return Decode(path, data, target)
}

// PermissionBits uses Discord's canonical snake-case permission names.
var PermissionBits = permissionBits()

func permissionBits() map[string]uint64 {
	names := strings.Fields(`create_instant_invite kick_members ban_members administrator manage_channels manage_guild add_reactions view_audit_log priority_speaker stream view_channel send_messages send_tts_messages manage_messages embed_links attach_files read_message_history mention_everyone use_external_emojis view_guild_insights connect speak mute_members deafen_members move_members use_vad change_nickname manage_nicknames manage_roles manage_webhooks manage_guild_expressions use_application_commands request_to_speak manage_events manage_threads create_public_threads create_private_threads use_external_stickers send_messages_in_threads use_embedded_activities moderate_members view_creator_monetization_analytics use_soundboard create_guild_expressions create_events use_external_sounds send_voice_messages`)
	bits := make(map[string]uint64, len(names)+5)
	for index, name := range names {
		bits[name] = uint64(1) << index
	}
	bits["send_polls"] = 1 << 49
	bits["use_external_apps"] = 1 << 50
	bits["pin_messages"] = 1 << 51
	bits["bypass_slowmode"] = 1 << 52
	return bits
}
