package runtime

import (
	"encoding/json"
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
