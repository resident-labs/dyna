package jobs

import (
	"fmt"
	"path"
	"time"

	"dyna-agent/internal/shellquote"
)

// AdmissionWait is the explicit queue bound a job accepts while it waits for a
// slot on its node.
const AdmissionWait = 55 * time.Minute

// Slots belong to jobs, across callers and processes. A waiter tries every
// slot before blocking on one slot without polling.
//
// SlotScript runs inner while holding one of the node's slots. A wrapper that
// waits for a slot installs its deadline and its signal trap first, and
// watches its own job while it waits: a job that is cancelled or removed
// leaves no queued wrapper holding a slot on the machine.
func SlotScript(base string, slots, index int, active string, deadline time.Duration, inner string) string {
	q := shellquote.Quote
	seconds := int64((deadline + time.Second - 1) / time.Second)
	return "set -euo pipefail; trap 'exit 143' HUP INT TERM; " +
		"mkdir -p " + q(path.Join(base, "slots")) + "; " +
		fmt.Sprintf("deadline=$(( $(date +%%s) + %d )); ", int(seconds)) +
		"acquired=0; " +
		fmt.Sprintf("for slot in $(seq 0 %d); do exec 9>\"%s/slots/$slot.lock\"; if flock -n 9; then acquired=1; break; fi; done; ", slots-1, base) +
		"if [ \"$acquired\" = 0 ]; then printf 'local CI: every slot is busy, waiting for any free slot\\n' >&2; " +
		"while [ \"$acquired\" = 0 ]; do " +
		// Every poll tries every slot, so a waiting job takes the first one that
		// frees up instead of queueing behind one named slot.
		fmt.Sprintf("for slot in $(seq 0 %d); do exec 9>\"%s/slots/$slot.lock\"; if flock -n 9; then acquired=1; break; fi; done; ", slots-1, base) +
		"[ \"$acquired\" = 0 ] || break; " +
		// The job holds this file, and it goes when the job is cancelled or
		// removed: a queued wrapper then has nothing left to wait for.
		"[ -e " + q(active) + " ] || exit 75; " +
		"[ \"$(date +%s)\" -lt \"$deadline\" ] || exit 75; " +
		"sleep ${DYNA_CI_SLOT_POLL:-1}; " +
		"done; fi; " +
		// A slot that came free after the job was removed must not start the
		// work: the job is checked once more right before its work begins.
		"[ -e " + q(active) + " ] || exit 75; " +
		"[ \"$(date +%s)\" -lt \"$deadline\" ] || exit 75; " + inner
}
