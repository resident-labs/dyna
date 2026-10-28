package runtime

import (
	"encoding/json"
	"fmt"
	"syscall"

	"dyna-agent/internal/processguard"
)

func init() {
	nativeUseProbe = func(raw json.RawMessage) (bool, error) {
		var ref processguard.Reference
		if err := json.Unmarshal(raw, &ref); err != nil {
			return true, err
		}
		return processguard.Probe(ref)
	}
	nativeUseSignal = func(raw json.RawMessage, sig syscall.Signal) error {
		var ref processguard.Reference
		if err := json.Unmarshal(raw, &ref); err != nil {
			return err
		}
		return processguard.Signal(ref, sig)
	}
}

// leaseHolder names the process that keeps a live use-lease: the guarded
// native harness process when the lease carries its guard reference, or the
// process that took the lease. A lease can read released and still live,
// because the probe follows the reference, not the flag.
func leaseHolder(use resourceUse) string {
	var ref processguard.Reference
	if json.Unmarshal(use.Reference, &ref) == nil {
		leader, _ := ref.Leader()
		return fmt.Sprintf("Native process %d (guard %d)", leader, ref.Guardian())
	}
	return fmt.Sprintf("PID %d", use.PID)
}
