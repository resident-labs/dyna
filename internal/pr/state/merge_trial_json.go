package state

import "encoding/json"

func (b *MergeTrial) UnmarshalJSON(data []byte) error {
	type trial MergeTrial
	var saved struct {
		trial
		// TODO: legacy/shim remove later — read rootNotified from batches saved before merge steward; remove after those batches expire.
		RootNotified bool `json:"rootNotified"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	*b = MergeTrial(saved.trial)
	if saved.RootNotified && !b.Notified {
		b.Notified, b.Notifier = true, "root"
	}
	return nil
}
