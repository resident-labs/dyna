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
