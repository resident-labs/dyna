package doctor

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Condition is a parsed --if expression, such as
// "load>2/core for 5m or memory>85%". Terms are joined by "or"; each term is
// clauses joined by "and", and "and" binds tighter. A clause has no spaces
// inside its comparison. Metrics:
//
//	load              machine 1-minute load average per core
//	memory            machine memory used, percent
//	pressure.cpu      machine PSI "some avg10", percent (also .memory, .io)
//	cores.<group>     cores a group used, such as cores.engine
//	memory.<group>    bytes a group holds, such as memory.agents>4G
//
// Without "for", the clause reads the newest sample. With "for D", every
// sample of the last D must match, and the samples must cover D.
type Condition struct {
	Text string
	any  [][]clause
}

type clause struct {
	metric, group string
	less          bool
	value         float64
	window        time.Duration
}

// ReadWindow returns one group's samples from since on, oldest first.
type ReadWindow func(group string, since time.Time) ([]Sample, error)

// ParseCondition parses an --if expression.
func ParseCondition(text string) (Condition, error) {
	c := Condition{Text: text}
	words := strings.Fields(text)
	term := []clause{}
	for i := 0; i < len(words); i++ {
		cl, err := parseClause(words[i])
		if err != nil {
			return Condition{}, err
		}
		if i+1 < len(words) && words[i+1] == "for" {
			if i+2 >= len(words) {
				return Condition{}, fmt.Errorf("%q: for needs a duration, such as 5m", text)
			}
			if cl.window, err = time.ParseDuration(words[i+2]); err != nil || cl.window < time.Minute {
				return Condition{}, fmt.Errorf("%q: for needs a duration of at least 1m, such as 5m", text)
			}
			i += 2
		}
		term = append(term, cl)
		if i+1 == len(words) {
			break
		}
		i++
		switch words[i] {
		case "and":
		case "or":
			c.any, term = append(c.any, term), []clause{}
		default:
			return Condition{}, fmt.Errorf("%q: expected and or or, got %q", text, words[i])
		}
		if i+1 == len(words) {
			return Condition{}, fmt.Errorf("%q ends with %q", text, words[i])
		}
	}
	if len(term) == 0 {
		return Condition{}, fmt.Errorf("the condition is empty")
	}
	c.any = append(c.any, term)
	return c, nil
}

func parseClause(word string) (clause, error) {
	i := strings.IndexAny(word, "<>")
	if i <= 0 || i == len(word)-1 {
		return clause{}, fmt.Errorf("%q is not a comparison such as load>2", word)
	}
	cl := clause{metric: word[:i], less: word[i] == '<'}
	raw := word[i+1:]
	if name, group, ok := strings.Cut(cl.metric, "."); ok && (name == "cores" || name == "memory") {
		cl.metric, cl.group = name, group
	}
	switch cl.metric {
	case "load":
		raw = strings.TrimSuffix(raw, "/core")
	case "memory", "pressure.cpu", "pressure.memory", "pressure.io":
		if cl.group == "" {
			raw = strings.TrimSuffix(raw, "%")
		}
	case "cores":
	default:
		return clause{}, fmt.Errorf("unknown metric %q; use load, memory, pressure.cpu, pressure.memory, pressure.io, cores.<group> or memory.<group>", cl.metric)
	}
	if cl.metric == "cores" && cl.group == "" {
		return clause{}, fmt.Errorf("cores needs a group, such as cores.engine")
	}
	scale := 1.0
	if cl.metric == "memory" && cl.group != "" {
		for suffix, n := range map[string]float64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30} {
			if strings.HasSuffix(raw, suffix) {
				raw, scale = strings.TrimSuffix(raw, suffix), n
			}
		}
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return clause{}, fmt.Errorf("%q: %q is not a number", word, word[i+1:])
	}
	cl.value = v * scale
	return cl, nil
}

// Holds reports whether the condition matches the samples at now, and the
// readings of the first term that matched.
func (c Condition) Holds(read ReadWindow, now time.Time) (bool, string, error) {
	for _, term := range c.any {
		var seen []string
		ok := true
		for _, cl := range term {
			match, reading, err := cl.holds(read, now)
			if err != nil {
				return false, "", err
			}
			if !match {
				ok = false
				break
			}
			seen = append(seen, reading)
		}
		if ok {
			return true, strings.Join(seen, ", "), nil
		}
	}
	return false, "", nil
}

// fresh is how old the newest sample may be before a clause without "for"
// counts as unknown.
const fresh = 2 * time.Minute

func (cl clause) holds(read ReadWindow, now time.Time) (bool, string, error) {
	group := cl.group
	if group == "" {
		group = "machine"
	}
	span := cl.window
	if span == 0 {
		span = fresh
	}
	samples, err := read(group, now.Add(-span))
	if err != nil || len(samples) == 0 {
		return false, "", err
	}
	if now.Sub(samples[len(samples)-1].At) > fresh {
		return false, "", nil
	}
	if cl.window == 0 {
		samples = samples[len(samples)-1:]
	} else if now.Sub(samples[0].At) < cl.window-RecordSlack {
		// Samples started inside the window; it is not covered yet.
		return false, "", nil
	}
	low, high := cl.of(samples[0]), cl.of(samples[0])
	for i, x := range samples {
		if i > 0 && x.At.Sub(samples[i-1].At) > fresh {
			return false, "", nil
		}
		v := cl.of(x)
		if cl.less && v >= cl.value || !cl.less && v <= cl.value {
			return false, "", nil
		}
		low, high = min(low, v), max(high, v)
	}
	reading := cl.format(high)
	if cl.less {
		reading = cl.format(low)
	}
	if cl.window > 0 {
		reading += " for " + cl.window.String()
	}
	return true, reading, nil
}

// RecordSlack lets a window count as covered when its first sample is up to
// one record interval after the window starts.
const RecordSlack = time.Minute

func (cl clause) of(x Sample) float64 {
	switch cl.metric {
	case "load":
		return x.Load
	case "memory":
		if cl.group != "" {
			return float64(x.Group.Memory)
		}
		if x.MemoryTotal == 0 {
			return 0
		}
		return float64(x.Group.Memory) / float64(x.MemoryTotal) * 100
	case "cores":
		return x.Group.Cores
	case "pressure.cpu":
		return x.Group.Pressure.CPU
	case "pressure.memory":
		return x.Group.Pressure.Memory
	default:
		return x.Group.Pressure.IO
	}
}

func (cl clause) format(v float64) string {
	switch {
	case cl.metric == "load":
		return fmt.Sprintf("load %.1f/core", v)
	case cl.metric == "memory" && cl.group != "":
		return fmt.Sprintf("%s memory %s", cl.group, bytes(uint64(v)))
	case cl.metric == "memory":
		return fmt.Sprintf("memory %.0f%%", v)
	case cl.metric == "cores":
		return fmt.Sprintf("%s %.1f cores", cl.group, v)
	default:
		return fmt.Sprintf("%s %.0f%%", cl.metric, v)
	}
}

// bytes formats n with a binary unit.
func bytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KiB", n>>10)
	}
}
