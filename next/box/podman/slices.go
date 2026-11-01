package podman

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const pressureWindow = 60 * time.Second

// diskScript prints the size of the filesystem at $1 and its use, which is size minus available, so reserved space counts as used.
// btrfs can run out of metadata with data space left, so under 2 GiB of metadata room nothing counts as available.
// Metadata room is the free part of its chunks plus half of the unallocated space. Any unreadable file leaves the plain df figures.
const diskScript = `addup() { cat "$@" 2>/dev/null | awk '{s += $1} END {printf "%.0f", s}'; return "${PIPESTATUS[0]}"; }
if disk=$(df -B1 --output=size,avail "$1" 2>/dev/null); then
  read -r size avail < <(echo "$disk" | awk 'NR == 2 {print $1, $2}')
  if [ "$(findmnt -no FSTYPE --target "$1" 2>/dev/null | head -1)" = btrfs ]; then
    sys=/sys/fs/btrfs/$(findmnt -no UUID --target "$1" 2>/dev/null | head -1)
    if [ ! -r "$sys/allocation/metadata/total_bytes" ]; then
      dev=$(readlink -f "$(findmnt -no SOURCE --target "$1" 2>/dev/null | head -1 | sed 's/\[.*//')")
      for dir in /sys/fs/btrfs/*/devices/"${dev##*/}"; do [ -n "$dev" ] && sys=${dir%/devices/*}; done
    fi
    devs=("$sys"/devices/*)
    if total=$(addup "$sys/allocation/metadata/total_bytes") && used=$(addup "$sys/allocation/metadata/bytes_used") && alloc=$(addup "$sys"/allocation/{data,metadata,system}/disk_total) && sectors=$(addup "${devs[@]/%//size}") && [ "$sectors" -gt 0 ]; then
      unalloc=$((sectors * 512 - alloc))
      [ "$unalloc" -gt 0 ] || unalloc=0
      [ $((total - used + unalloc / 2)) -ge $((2 << 30)) ] || avail=0
    fi
  fi
  [ -z "$size" ] || echo "dyna-disk $size $((size - avail))"
fi
`

// heartbeatScript prints memory.events.local under dyna.slice. grep exits 1 when nothing matches, which is not a failure.
const heartbeatScript = diskScript + `cg=$(systemctl --user show dyna.slice -p ControlGroup --value) || exit 1; [ -n "$cg" ] || { echo dyna-load 0 0; exit 0; }; dir="/sys/fs/cgroup$cg"; mem=$(awk '$1 == "anon" || $1 == "shmem" {sum += $2; n++} END {if (n != 2) exit 1; printf "%.0f", sum}' "$dir/memory.stat") || exit 1; swap=$(cat "$dir/memory.swap.current") || exit 1; mem=$((mem + swap)); cpu=$(awk '$1 == "usage_usec" {print $2}' "$dir/cpu.stat") || exit 1; [ -n "$cpu" ] || exit 1; echo "dyna-load $mem $cpu"; grep -r --include=memory.events.local . "$dir" || [ $? = 1 ]`

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
