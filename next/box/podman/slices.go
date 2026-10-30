package podman

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const pressureWindow = 60 * time.Second

// heartbeatScript prints memory.events.local under dyna.slice. grep exits 1 when nothing matches, which is not a failure.
const heartbeatScript = `cg=$(systemctl --user show dyna.slice -p ControlGroup --value) || exit 1; [ -n "$cg" ] || exit 0; grep -r --include=memory.events.local . "/sys/fs/cgroup$cg" || [ $? = 1 ]`

type slicePressure struct {
	prev      map[string]int64
	high, max episode
}

// episode is one run of counter rises. A quiet period ends it, and it reports once.
type episode struct {
	start, last time.Time
	reported    bool
}

// rise reports true once per episode, when its rises span at least need.
func (e *episode) rise(now time.Time, need time.Duration) bool {
	if e.last.IsZero() || now.Sub(e.last) >= pressureWindow {
		*e = episode{start: now}
	}
	e.last = now
	if e.reported || now.Sub(e.start) < need {
		return false
	}
	e.reported = true
	return true
}

// observeSlices calls onPressure for each due episode. A slice's first sample, or a sample where a counter fell, only sets its baseline.
func observeSlices(seen map[string]*slicePressure, events map[string]map[string]int64, now time.Time, onPressure func(slice string, kind contract.PressureKind)) {
	for name := range seen {
		if _, ok := events[name]; !ok {
			delete(seen, name)
		}
	}
	for name, counts := range events {
		state, ok := seen[name]
		if ok {
			for key, prev := range state.prev {
				ok = ok && counts[key] >= prev
			}
		}
		if !ok {
			seen[name] = &slicePressure{prev: counts}
			continue
		}
		prev := state.prev
		state.prev = counts
		if counts["high"] > prev["high"] && state.high.rise(now, pressureWindow) {
			onPressure(name, contract.PressureHigh)
		}
		if (counts["max"] > prev["max"] || counts["oom_kill"] > prev["oom_kill"]) && state.max.rise(now, 0) {
			onPressure(name, contract.PressureMax)
		}
	}
}

// parseSliceEvents maps each dyna slice to its memory.events.local counters. The slice is the cgroup directory of the file.
func parseSliceEvents(out []byte) (map[string]map[string]int64, error) {
	events := make(map[string]map[string]int64)
	for _, line := range strings.Split(string(out), "\n") {
		path, rest, ok := strings.Cut(line, ":")
		fields := strings.Fields(rest)
		slice := filepath.Base(filepath.Dir(path))
		if !ok || len(fields) != 2 || !strings.HasSuffix(slice, ".slice") {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, err
		}
		if events[slice] == nil {
			events[slice] = make(map[string]int64)
		}
		events[slice][fields[0]] = value
	}
	return events, nil
}
