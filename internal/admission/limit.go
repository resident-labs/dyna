package admission

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Defaults of the automatic limit. They describe a turn in general: a harness
// process with its tools holds about a gibibyte, and about four turns per core
// keep a machine busy without stalling it.
const (
	PerTurnBytes   = 1 << 30
	TurnsPerCore   = 4
	MinReserve     = 2 << 30
	reserveDivisor = 10 // keep a tenth of memory free
	// OverloadLoad is the load per core above which no new turn starts.
	OverloadLoad = 2.0
	floorTurns   = 1
)

// Policy is the owner's setting.
type Policy struct {
	// MaxTurns is a fixed limit. Zero means automatic, and Off turns the gate off.
	MaxTurns int
	Off      bool
	// Reserve is the memory kept free; zero means a tenth of the machine, at
	// least MinReserve.
	Reserve uint64
}

// Readings is what the machine looks like now.
type Readings struct {
	Cores    int
	Load1    float64
	MemTotal uint64
	MemAvail uint64
}

// Limit is the most turns of one pool that may run, given the turns that run
// now. An automatic limit adds the turns the free memory can still take to the
// turns that already run, and adds none while the machine is overloaded. At
// least one turn always runs, so work never stops altogether.
func Limit(p Policy, r Readings, held int) int {
	if p.Off {
		return -1
	}
	if p.MaxTurns > 0 {
		return p.MaxTurns
	}
	hard := max(r.Cores, 1) * TurnsPerCore
	if r.Cores > 0 && r.Load1/float64(r.Cores) > OverloadLoad {
		return max(held, floorTurns)
	}
	reserve := p.Reserve
	if reserve == 0 {
		reserve = max(r.MemTotal/reserveDivisor, MinReserve)
	}
	room := 0
	if r.MemAvail > reserve {
		room = int((r.MemAvail - reserve) / PerTurnBytes)
	}
	return min(max(held+room, floorTurns), hard)
}

// RoomShare is the most slots one room may hold of a pool whose limit is
// limit: half, so another room can always start. A pool of one or two slots
// leaves a room one.
func RoomShare(limit int) int { return max(limit/2, 1) }

// Read reads the machine's load and memory from /proc. A machine without those
// files returns an error, and the caller leaves the gate open.
func Read() (Readings, error) {
	r := Readings{Cores: runtime.NumCPU()}
	loadavg, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return Readings{}, err
	}
	if _, err := fmt.Sscanf(string(loadavg), "%f", &r.Load1); err != nil {
		return Readings{}, err
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Readings{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			r.MemTotal = kb << 10
		case "MemAvailable":
			r.MemAvail = kb << 10
		}
	}
	if r.MemTotal == 0 {
		return Readings{}, fmt.Errorf("no MemTotal in /proc/meminfo")
	}
	return r, scanner.Err()
}
