package folder

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/resident-labs/dyna/engine/discord/policy"
)

// Load reads only the managed part of the tree. A marker is checked before any
// descendant is opened, including category.yaml and channel.yaml.
func Load(path string) (Folder, error) {
	var result Folder
	if err := rejectLinks(path); err != nil {
		return result, err
	}
	if err := readYAML(filepath.Join(path, "server.yaml"), &result.Server); err != nil {
		return result, err
	}
	data, err := os.ReadFile(filepath.Join(path, "policy.yaml"))
	if err == nil {
		result.Policy = string(data)
		if _, err := policy.Decode(result.Policy); err != nil {
			return result, fmt.Errorf("policy.yaml: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, err
	}
	_, err = os.Stat(filepath.Join(path, "hooks.yaml"))
	result.Hooks = err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return result, err
	}
	channels := filepath.Join(path, "channels")
	entries, err := os.ReadDir(channels)
	if errors.Is(err, fs.ErrNotExist) {
		return result, validate(result)
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return result, fmt.Errorf("%s: expected a category or channel folder", entry.Name())
		}
		loaded, err := loadEntry(channels, entry.Name(), "")
		if err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, loaded...)
	}
	return result, validate(result)
}

func loadEntry(root, name, parent string) ([]Entry, error) {
	relative := filepath.Join(parent, name)
	path := filepath.Join(root, relative)
	entry := Entry{Path: relative, Name: name, Parent: parent}
	note, err := os.ReadFile(filepath.Join(path, "UNMANAGED.md"))
	if err == nil {
		entry.Unmanaged = strings.SplitN(string(note), "\n", 2)[0]
		// An empty marker still means unmanaged.
		if entry.Unmanaged == "" {
			entry.Unmanaged = "(no note)"
		}
		return []Entry{entry}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_, err = os.Stat(filepath.Join(path, "category.yaml"))
	if err == nil {
		if parent != "" {
			return nil, fmt.Errorf("%s: nested categories are not supported", path)
		}
		entry.Category = &Category{}
		if err := readYAML(filepath.Join(path, "category.yaml"), entry.Category); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	} else {
		entry.Channel = &Channel{Type: "text"}
		err := readYAML(filepath.Join(path, "channel.yaml"), entry.Channel)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if entry.Channel.Type == "" {
			entry.Channel.Type = "text"
		}
	}
	children, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := []Entry{entry}
	for _, child := range children {
		if child.IsDir() {
			if entry.Category == nil {
				return nil, fmt.Errorf("%s: channels cannot contain folders", path)
			}
			loaded, err := loadEntry(root, child.Name(), relative)
			if err != nil {
				return nil, err
			}
			result = append(result, loaded...)
		} else if strings.HasSuffix(child.Name(), ".md") {
			if entry.Channel == nil {
				return nil, fmt.Errorf("%s: message must be inside a channel", path)
			}
			messagePath := filepath.Join(path, child.Name())
			data, err := os.ReadFile(messagePath)
			if err != nil {
				return nil, err
			}
			message, err := parseMessage(messagePath, data)
			if err != nil {
				return nil, err
			}
			if result[0].Messages == nil {
				result[0].Messages = map[string]Message{}
			}
			result[0].Messages[child.Name()] = message
		} else if child.Name() != "category.yaml" && child.Name() != "channel.yaml" {
			return nil, fmt.Errorf("%s: unknown file %s", path, child.Name())
		}
	}
	return result, nil
}

// Refuse symlinks in managed trees; a marker stops traversal before descendants.
func rejectLinks(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are refused", path)
		}
		if entry.IsDir() {
			marker, err := os.Lstat(filepath.Join(path, "UNMANAGED.md"))
			if err == nil {
				if !marker.Mode().IsRegular() {
					return fmt.Errorf("%s: UNMANAGED.md must be a regular file", path)
				}
				return filepath.SkipDir
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

func validate(folder Folder) error {
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, role := range folder.Server.Roles {
		if role.Name == "" || names[role.Name] {
			return fmt.Errorf("server.yaml: role name %q is missing or not unique", role.Name)
		}
		names[role.Name] = true
		if err := identity(role.Identity, ids); err != nil {
			return fmt.Errorf("server.yaml: %w", err)
		}
	}
	for _, rule := range folder.Server.AutoMod {
		if err := identity(rule.Identity, ids); err != nil {
			return fmt.Errorf("server.yaml: %w", err)
		}
	}
	for _, entry := range folder.Entries {
		if entry.Unmanaged != "" {
			continue
		}
		if entry.Channel != nil {
			channel := entry.Channel
			if err := identity(channel.Identity, ids); err != nil {
				return fmt.Errorf("%s/channel.yaml: %w", entry.Path, err)
			}
			switch channel.Type {
			case "text", "voice", "announcement", "forum":
			default:
				return fmt.Errorf("%s/channel.yaml: unknown type %q", entry.Path, channel.Type)
			}
			if channel.Slowmode != "" {
				duration, err := time.ParseDuration(channel.Slowmode)
				if err != nil || duration < 0 || duration > 6*time.Hour || duration%time.Second != 0 {
					return fmt.Errorf("%s/channel.yaml: slowmode must be whole seconds from 0s to 6h", entry.Path)
				}
			}
		} else if entry.Category != nil {
			if err := identity(entry.Category.Identity, ids); err != nil {
				return fmt.Errorf("%s/category.yaml: %w", entry.Path, err)
			}
		}
	}
	return validateReferences(folder)
}

func identity(identity Identity, ids map[string]bool) error {
	if identity.ID != "" && identity.Was != "" {
		return fmt.Errorf("id and was cannot both be set")
	}
	if identity.ID != "" {
		for _, char := range identity.ID {
			if char < '0' || char > '9' {
				return fmt.Errorf("invalid Discord id %q", identity.ID)
			}
		}
		if ids[identity.ID] {
			return fmt.Errorf("id %s adopted more than once", identity.ID)
		}
		ids[identity.ID] = true
	}
	return nil
}
