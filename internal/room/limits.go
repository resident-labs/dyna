package room

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Limits are a room's resource slice limits, stored on the room record in
// SQLite with the room's other settings. Each limit is default (use the
// machine default), off (no limit), or a set value. The kernel enforces them
// on the room's own systemd slice, so one room cannot take the machine or the
// other rooms down.
type Limits struct {
	// MemoryHigh is where the kernel starts to throttle and reclaim the
	// room's processes instead of killing them.
	MemoryHigh Limit `json:"memoryHigh,omitempty"`
	// MemoryMax is the hard memory limit: the OOM killer takes a process
	// inside the room, never outside it.
	MemoryMax Limit `json:"memoryMax,omitempty"`
	// TasksMax is the most processes and threads the room may run.
	TasksMax Limit `json:"tasksMax,omitempty"`
}

// Limit is one limit of a room's slice: the zero value uses the machine
// default, Off removes the limit, and a positive Value replaces the default
// (bytes for memory, a count for tasks).
type Limit struct {
	Off   bool  `json:"off,omitempty"`
	Value int64 `json:"value,omitempty"`
}

// LimitField names one of a room's limits.
type LimitField string

const (
	LimitMemoryHigh LimitField = "memoryHigh"
	LimitMemoryMax  LimitField = "memoryMax"
	LimitTasksMax   LimitField = "tasksMax"
)

// ParseLimitField reads one limit name, as the CLI and the app accept it.
func ParseLimitField(name string) (LimitField, error) {
	switch field := LimitField(strings.TrimSpace(name)); field {
	case LimitMemoryHigh, LimitMemoryMax, LimitTasksMax:
		return field, nil
	}
	return "", fmt.Errorf("unknown room limit %q; limits.memoryHigh, limits.memoryMax and limits.tasksMax are the limits this command changes", strings.TrimSpace(name))
}

// Label is the field's name in messages, such as "memory high".
func (f LimitField) Label() string {
	switch f {
	case LimitMemoryHigh:
		return "memory high"
	case LimitMemoryMax:
		return "memory max"
	default:
		return "tasks"
	}
}

// Parse reads one limit value for this field, as the CLI and the app accept
// it: "default" or empty keeps the machine default, "off" removes the limit,
// and a size such as 2G for memory or a count such as 512 for tasks sets it.
func (f LimitField) Parse(value string) (Limit, error) {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "default":
		return Limit{}, nil
	case "off":
		return Limit{Off: true}, nil
	}
	if f == LimitTasksMax {
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || n <= 0 {
			return Limit{}, fmt.Errorf("%s must be a count such as 512, off or default, not %q", f.Label(), trimmed)
		}
		return Limit{Value: n}, nil
	}
	bytes, ok := parseBytes(trimmed)
	if !ok {
		return Limit{}, fmt.Errorf("%s must be a size such as 512M or 2G, off or default, not %q", f.Label(), trimmed)
	}
	return Limit{Value: bytes}, nil
}

// Format is the value the CLI and the app show for this field's limit.
func (f LimitField) Format(l Limit) string {
	switch {
	case l.Off:
		return "off"
	case l.Value > 0 && f == LimitTasksMax:
		return strconv.FormatInt(l.Value, 10)
	case l.Value > 0:
		return formatBytes(l.Value)
	default:
		return "default"
	}
}

// LimitOf returns one of the limits by field.
func (l Limits) LimitOf(field LimitField) Limit {
	switch field {
	case LimitMemoryHigh:
		return l.MemoryHigh
	case LimitMemoryMax:
		return l.MemoryMax
	default:
		return l.TasksMax
	}
}

// Set stores one of the limits, leaving the others as they are.
func (l *Limits) Set(field LimitField, limit Limit) {
	switch field {
	case LimitMemoryHigh:
		l.MemoryHigh = limit
	case LimitMemoryMax:
		l.MemoryMax = limit
	default:
		l.TasksMax = limit
	}
}

// parseBytes reads a size the way systemd reads a memory limit: a count of
// bytes, or a count with a K, M, G or T suffix (1024-based, case-insensitive,
// with an optional iB or B), such as 512M or 2G.
func parseBytes(raw string) (int64, bool) {
	s := strings.TrimSuffix(strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(raw)), "IB"), "B")
	unit := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K':
			unit, s = 1<<10, s[:n-1]
		case 'M':
			unit, s = 1<<20, s[:n-1]
		case 'G':
			unit, s = 1<<30, s[:n-1]
		case 'T':
			unit, s = 1<<40, s[:n-1]
		}
	}
	count, err := strconv.ParseInt(s, 10, 64)
	if err != nil || count <= 0 || count > (1<<62)/unit {
		return 0, false
	}
	return count * unit, true
}

// formatBytes writes a byte count as it was typed: the largest unit that
// divides it exactly, such as 2G or 512M, or plain bytes.
func formatBytes(bytes int64) string {
	for _, u := range []struct {
		size   int64
		suffix string
	}{{1 << 40, "T"}, {1 << 30, "G"}, {1 << 20, "M"}, {1 << 10, "K"}} {
		if bytes >= u.size && bytes%u.size == 0 {
			return strconv.FormatInt(bytes/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(bytes, 10)
}

// SetLimit stores one of the room's resource limits. ref is a name or ID.
func (s *Store) SetLimit(ctx context.Context, ref string, field LimitField, limit Limit) (Record, error) {
	resolved, err := s.Resolve(ref)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = s.WithRoomSnapshot(ctx, resolved.ID, func(rec Record) error {
		rec.Limits.Set(field, limit)
		out = rec
		return s.writeRecord(rec)
	})
	return out, err
}
