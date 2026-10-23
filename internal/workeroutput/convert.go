package workeroutput

import "slices"

// ConvertSessionTurn gives the retained events of one session turn their
// current shape, in place, and returns the indexes it changed in order. The
// output projection migration runs it once per turn, so readers repair
// nothing. dialect is the turn's recorded output dialect.
// TODO: legacy/shim remove later — converts output written before the current
// event shapes; remove with the render-time repairs in transcript.
func ConvertSessionTurn(dialect string, events []Event) []int {
	changed := map[int]bool{}
	streamed := dialect == DialectAntigravityStreamJSON || dialect == DialectGrokJSON || dialect == DialectMuseJSONL
	started := map[string]bool{}
	for i := range events {
		e := &events[i]
		switch {
		case e.Kind == "status" && e.Name == "compact_boundary":
			e.Name = "compaction"
			changed[i] = true
		case streamed && e.Kind == "message" && e.Role == "assistant" && e.State == "":
			// A result response is a full message, not a streamed delta.
			if i == 0 || events[i-1].Kind != "status" || events[i-1].Name != "result" {
				e.State = "delta"
				changed[i] = true
			}
		case dialect == DialectGrokJSON && e.Kind == "tool" && e.State == "started" && e.ID != "":
			// Grok wrote progress on a running call as a nameless start.
			if started[e.ID] && e.Name == "" {
				e.State = "updated"
				changed[i] = true
			}
			started[e.ID] = true
		}
	}
	pairByName(events, changed)
	out := make([]int, 0, len(changed))
	for i := range changed {
		out = append(out, i)
	}
	slices.Sort(out)
	return out
}

// pairByName gives a completion whose ID matches no open start the ID of the
// oldest open start with its tool name. Early Muse output keyed starts by task
// ID and results by call ID; same-name calls completed in start order. The
// walk follows PairTools, so the pairs are the ones readers showed.
func pairByName(events []Event, changed map[int]bool) {
	open := map[string]int{}
	openByName := map[string][]int{}
	for i := range events {
		e := &events[i]
		if e.Kind != "tool" || e.ID == "" {
			continue
		}
		switch e.State {
		case "started", "updated":
			if _, ok := open[e.ID]; ok {
				if e.State == "updated" {
					continue
				}
			} else {
				openByName[e.Name] = append(openByName[e.Name], i)
			}
			open[e.ID] = i
		case "completed", "failed":
			start, ok := open[e.ID]
			if !ok {
				queue := openByName[e.Name]
				if len(queue) == 0 {
					continue
				}
				start = queue[0]
				e.ID = events[start].ID
				changed[i] = true
			}
			delete(open, e.ID)
			openByName[events[start].Name] = slices.DeleteFunc(openByName[events[start].Name], func(k int) bool { return k == start })
		}
	}
}

// ConvertRunAgent restores tool fields in retained workflow output.
// TODO: legacy/shim remove later — remove after retained workflow artifacts expire.
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
