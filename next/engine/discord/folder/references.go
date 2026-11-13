package folder

import (
	"fmt"
	"strings"
)

func validateReferences(folder Folder) error {
	roles := map[string]int{"everyone": 1}
	channels := map[string]int{}
	categories := map[string]int{}
	for _, role := range folder.Server.Roles {
		roles[role.Name]++
	}
	listedCategories := map[string]bool{}
	for _, name := range folder.Server.Categories {
		listedCategories[name] = true
	}
	children := map[string]map[string]int{}
	for _, entry := range folder.Entries {
		isCategory := entry.Category != nil || (entry.Parent == "" && entry.Unmanaged != "" && listedCategories[entry.Name])
		if entry.Parent == "" && isCategory {
			categories[entry.Name]++
		}
		if entry.Channel != nil || (entry.Unmanaged != "" && !isCategory) {
			channels[entry.Name]++
		}
		if children[entry.Parent] == nil {
			children[entry.Parent] = map[string]int{}
		}
		children[entry.Parent][entry.Name]++
	}
	if err := orderedNames("server.yaml", folder.Server.Categories, categories); err != nil {
		return err
	}
	for _, entry := range folder.Entries {
		if entry.Unmanaged != "" {
			continue
		}
		var overwrites Overwrites
		if entry.Category != nil {
			overwrites = entry.Category.Overwrites
			if err := orderedNames(entry.Path+"/category.yaml", entry.Category.Channels, children[entry.Path]); err != nil {
				return err
			}
		} else {
			overwrites = entry.Channel.Overwrites
		}
		for _, values := range []map[string][]string{overwrites.Allow, overwrites.Deny} {
			for name := range values {
				if member, ok := strings.CutPrefix(name, "member:"); ok {
					if member == "" {
						return fmt.Errorf("%s: member ID is missing", entry.Path)
					}
					for _, char := range member {
						if char < '0' || char > '9' {
							return fmt.Errorf("%s: invalid member ID %s", entry.Path, member)
						}
					}
				} else if roles[name] != 1 {
					return fmt.Errorf("%s: role %q is missing or not unique", entry.Path, name)
				}
			}
		}
	}
	if community := folder.Server.Settings.Community; community != nil {
		for _, name := range []string{community.Rules, community.Updates} {
			if channels[name] != 1 {
				return fmt.Errorf("server.yaml: community channel %q is missing or not unique", name)
			}
		}
	}
	automodNames := map[string]bool{}
	for _, rule := range folder.Server.AutoMod {
		if rule.Name == "" || automodNames[rule.Name] {
			return fmt.Errorf("server.yaml: automod name %q is missing or not unique", rule.Name)
		}
		automodNames[rule.Name] = true
		if rule.Alert != "" && channels[rule.Alert] != 1 {
			return fmt.Errorf("server.yaml: alert channel %q is missing or not unique", rule.Alert)
		}
	}
	return nil
}

func orderedNames(path string, names []string, available map[string]int) error {
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || available[name] != 1 {
			return fmt.Errorf("%s: ordered name %q is missing or not unique", path, name)
		}
		seen[name] = true
	}
	return nil
}
