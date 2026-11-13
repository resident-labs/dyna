package folder

import "fmt"

func planAutoMod(plan *Plan, desired, live []AutoMod) error {
	matched := map[string]bool{}
	for _, rule := range desired {
		candidates := []AutoMod{}
		for _, current := range live {
			if matches(rule.Identity, rule.Name, current.ID, current.Name) {
				candidates = append(candidates, current)
			}
		}
		if len(candidates) > 1 {
			return fmt.Errorf("server.yaml: automod rule %q is not unique", rule.Name)
		}
		if len(candidates) == 0 {
			if rule.ID != "" || rule.Was != "" {
				return fmt.Errorf("server.yaml: adopted automod rule %q is missing", rule.Name)
			}
			plan.Changes = append(plan.Changes, Change{Kind: "automod", Name: rule.Name, Field: "create", After: rule})
			continue
		}
		current := candidates[0]
		id := current.ID
		if matched[id] {
			return fmt.Errorf("server.yaml: automod rule %s adopted twice", id)
		}
		matched[id] = true
		rule.Identity, current.Identity = Identity{}, Identity{}
		compare(plan, "automod", rule.Name, id, rule, current)
	}
	for _, rule := range live {
		if !matched[rule.ID] {
			plan.Lines = append(plan.Lines, fmt.Sprintf("AutoMod rule %s is on the server, not in the folder.", rule.Name))
		}
	}
	return nil
}
