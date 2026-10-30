package runtime

import (
	"log"

	"github.com/resident-labs/dyna/contract"
)

// pressure records a box.pressure event for the live box that owns a budget slice on a host.
func (b *Boxes) pressure(host contract.HostName) func(string, contract.PressureKind) {
	return func(slice string, kind contract.PressureKind) {
		records, err := b.Store.Boxes(b.Context)
		if err != nil {
			log.Printf("box pressure on host %s: %v", host, err)
			return
		}
		for _, record := range records {
			if record.Host != host || record.Slice != slice || (record.State != contract.BoxStarting && record.State != contract.BoxReady) {
				continue
			}
			if err := b.Pressure.RecordPressure(b.Context, record.ID, kind); err != nil {
				log.Printf("box pressure on host %s: %v", host, err)
			}
			return
		}
	}
}
