package folder

import (
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/engine/discord/policy"
)

func planPolicy(plan *Plan, desired, current string) error {
	if desired == "" {
		return nil
	}
	wanted, err := policy.Decode(desired)
	if err != nil {
		return fmt.Errorf("policy.yaml: %w", err)
	}
	if current != "" {
		existing, err := policy.Decode(current)
		if err != nil {
			return fmt.Errorf("enabled policy: %w", err)
		}
		sameEnabled := wanted.On() == existing.On()
		wanted.Enabled, existing.Enabled = nil, nil
		// Policy contains only JSON-supported scalar, struct and slice fields.
		wantedJSON, _ := json.Marshal(wanted)
		existingJSON, _ := json.Marshal(existing)
		if sameEnabled && string(wantedJSON) == string(existingJSON) {
			plan.Unchanged++
			return nil
		}
	}
	plan.Changes = append(plan.Changes, Change{Kind: PolicyKind, Operation: ChangeFields, Name: "policy.yaml", Field: "policy", After: desired})
	return nil
}
