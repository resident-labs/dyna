package workeroutput

import "slices"

// ConvertSessionTurn gives the retained events of one session turn their
// current shape, in place, and returns the indexes it changed in order. The
// output projection migration runs it once per turn, so readers repair
// nothing.
// TODO: legacy/shim remove later — converts output written before the current
// event shapes; remove with the render-time repairs in transcript.
func ConvertSessionTurn(events []Event) []int {
	changed := map[int]bool{}
	for i := range events {
		e := &events[i]
		if e.Kind == "status" && e.Name == "compact_boundary" {
			e.Name = "compaction"
			changed[i] = true
		}
	}
	out := make([]int, 0, len(changed))
	for i := range changed {
		out = append(out, i)
	}
	slices.Sort(out)
	return out
}

// ConvertRunAgent restores tool fields in retained workflow output.
// TODO: legacy/shim remove later — remove after retained workflow artifacts
// expire or migrate.
func ConvertRunAgent(events []Event) []int {
	changed := []int{}
	for i, event := range events {
		upgraded := UpgradeLegacyEvent(event)
		sameExit := upgraded.Exit == nil && event.Exit == nil ||
			upgraded.Exit != nil && event.Exit != nil && *upgraded.Exit == *event.Exit
		if upgraded.Input != event.Input || upgraded.Output != event.Output || !sameExit {
			events[i] = upgraded
			changed = append(changed, i)
		}
	}
	return changed
}
