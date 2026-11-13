package folder

import "fmt"

func resolveParents(desired, live []Entry) map[string]string {
	resolved := map[string]string{"": ""}
	for _, entry := range desired {
		if entry.Category == nil {
			continue
		}
		resolved[entry.Name] = entry.Name
		for _, current := range live {
			if current.Category != nil && matches(entry.Category.Identity, entry.Name, entryID(current), current.Name) {
				resolved[entry.Name] = current.Name
			}
		}
	}
	return resolved
}

func planEntries(plan *Plan, desired, live []Entry) error {
	parents := resolveParents(desired, live)
	matched, ignoredParents := map[string]bool{}, map[string]bool{}
	for _, entry := range desired {
		if entry.Unmanaged == "" {
			continue
		}
		plan.Lines = append(plan.Lines, fmt.Sprintf("%s is unmanaged: %s", entry.Path, entry.Unmanaged))
		for _, current := range live {
			if entry.Path == current.Path {
				matched[entryID(current)] = true
				plan.resolve(Subject{kindOf(current), entry.Path}, entryID(current))
				if current.Category != nil {
					ignoredParents[current.Path] = true
				}
			}
		}
	}
	for _, entry := range desired {
		if entry.Unmanaged != "" {
			continue
		}
		candidates := []Entry{}
		for _, current := range live {
			if ignoredParents[current.Parent] || (entry.Category == nil) != (current.Category == nil) {
				continue
			}
			if matches(EntryIdentity(entry), entry.Name, entryID(current), current.Name) && (EntryIdentity(entry).ID != "" || parents[entry.Parent] == current.Parent) {
				candidates = append(candidates, current)
			}
		}
		if len(candidates) > 1 {
			return fmt.Errorf("%s: name is not unique", entry.Path)
		}
		kind := kindOf(entry)
		if len(candidates) == 0 {
			identity := EntryIdentity(entry)
			if identity.ID != "" || identity.Was != "" {
				return fmt.Errorf("%s: adopted %s is missing", entry.Path, kind)
			}
			plan.Changes = append(plan.Changes, Change{Kind: kind, Operation: Create, Name: entry.Path, After: entry})
			planMessages(plan, entry, Entry{})
			continue
		}
		current := candidates[0]
		id := entryID(current)
		if matched[id] {
			return fmt.Errorf("%s: object %s is already adopted or unmanaged", entry.Path, id)
		}
		matched[id] = true
		plan.resolve(Subject{kind, entry.Path}, id)
		compare(plan, kind, entry.Path, id, struct{ Name, Parent string }{entry.Name, parents[entry.Parent]}, struct{ Name, Parent string }{current.Name, current.Parent})
		if entry.Category != nil {
			wanted, existing := *entry.Category, *current.Category
			wanted.Identity, existing.Identity = Identity{}, Identity{}
			compare(plan, kind, entry.Path, id, wanted, existing)
		} else {
			wanted, existing := *entry.Channel, *current.Channel
			wanted.Identity, existing.Identity = Identity{}, Identity{}
			compare(plan, kind, entry.Path, id, wanted, existing)
		}
		planMessages(plan, entry, current)
	}
	for _, entry := range live {
		if !matched[entryID(entry)] && !ignoredParents[entry.Parent] {
			plan.Lines = append(plan.Lines, fmt.Sprintf("%s is on the server, not in the folder.", entry.Path))
		}
	}
	return nil
}

func kindOf(entry Entry) Kind {
	if entry.Category != nil {
		return CategoryKind
	}
	return ChannelKind
}
