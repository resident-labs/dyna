package folder

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RecordID writes the Discord ID of an object apply created into the folder, as
// "id:", so that later hand moves and renames are matched by ID. name is the
// object's name in server.yaml for a role or AutoMod rule, and its path under
// channels/ for a category or channel.
func RecordID(root string, kind Kind, name, id string) error {
	var file, list string
	switch kind {
	case RoleKind:
		file, list = "server.yaml", "roles"
	case AutoModKind:
		file, list = "server.yaml", "automod"
	case CategoryKind:
		file = filepath.Join("channels", name, "category.yaml")
	case ChannelKind:
		file = filepath.Join("channels", name, "channel.yaml")
	default:
		return fmt.Errorf("%s objects have no id in the folder", kind)
	}
	path := filepath.Join(root, file)
	var document yaml.Node
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && kind == ChannelKind {
		data, err = []byte("{}\n"), nil // a plain text channel has no channel.yaml
	}
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	target := document.Content[0]
	if list != "" {
		target = itemNamed(target, list, name)
		if target == nil {
			return fmt.Errorf("%s: %s %q not found", path, kind, name)
		}
	}
	idKey := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"}
	idValue := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: id}
	target.Content = append([]*yaml.Node{idKey, idValue}, target.Content...)
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return err
	}
	return replaceFile(path, out.Bytes())
}

// itemNamed finds the entry of the sequence under key whose name is name.
func itemNamed(mapping *yaml.Node, key, name string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != key {
			continue
		}
		for _, item := range mapping.Content[i+1].Content {
			for j := 0; j+1 < len(item.Content); j += 2 {
				if item.Content[j].Value == "name" && item.Content[j+1].Value == name {
					return item
				}
			}
		}
	}
	return nil
}

// replaceFile never leaves a half-written folder file.
func replaceFile(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
