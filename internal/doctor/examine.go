package doctor

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Limits past which a machine is unhealthy.
const (
	maxLoad     = 2.0  // 1-minute load average per core
	maxMemory   = 85.0 // percent of memory used
	maxPressure = 10.0 // PSI "some avg10", percent
)

// Baseline is how far back "since" readings look.
const Baseline = 30 * time.Minute

// Finding is one cause with its numbers and, when one exists, the exact next
// command.
type Finding struct {
	Cause    string `json:"cause"`
	Evidence string `json:"evidence"`
	Command  string `json:"command,omitempty"`
}

// Report is one examination.
type Report struct {
	Delivered   bool      `json:"delivered"`
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Machine     string    `json:"machine"`
	Target      string    `json:"target"`
	Trigger     string    `json:"trigger"`
	Condition   string    `json:"condition,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Healthy     bool      `json:"healthy"`
	Summary     string    `json:"summary"`
	Findings    []Finding `json:"findings,omitempty"`
}

// Text is the report as the owner reads it.
func (r Report) Text() string {
	var b strings.Builder
	b.WriteString(r.Summary)
	for i, f := range r.Findings {
		fmt.Fprintf(&b, "\n%d. %s. %s.", i+1, f.Cause, f.Evidence)
		if f.Command != "" {
			fmt.Fprintf(&b, " Next: `%s`", f.Command)
		}
	}
	return b.String()
}

// resource is one kind of load the doctor ranks consumers by.
type resource struct {
	name  string
	of    func(Sample) float64
	unit  func(float64) string
	sort  string // the ps sort key for the outside-Dyna command
	share bool   // the machine total is measured, so shares mean something
}

var (
	cpu    = resource{"CPU", func(x Sample) float64 { return x.Group.Cores }, func(v float64) string { return fmt.Sprintf("%.1f cores", v) }, "-pcpu", true}
	memory = resource{"memory", func(x Sample) float64 { return float64(x.Group.Memory) }, func(v float64) string { return bytes(uint64(v)) }, "-rss", true}
	disk   = resource{"IO", func(x Sample) float64 { return x.Group.IO }, func(v float64) string { return bytes(uint64(v)) + "/s" }, "", false}
)

// ExamineMachine diagnoses machine from its stored samples at now.
func ExamineMachine(s Samples, machine string, now time.Time) (Report, error) {
	latest, err := s.Latest(machine)
	if err != nil {
		return Report{}, err
	}
	var m Sample
	for _, x := range latest {
		if x.Group.Name == "machine" {
			m = x
		}
	}
	if m.At.IsZero() || now.Sub(m.At) > fresh {
		return Report{}, fmt.Errorf("no samples of %s in the last %s; the engine records them each minute, so check that it runs with `systemctl --user status dyna-engine.service`", machine, fresh)
	}
	r := Report{At: now, Machine: machine, Target: "machine"}
	memPct := 0.0
	if m.MemoryTotal > 0 {
		memPct = float64(m.Group.Memory) / float64(m.MemoryTotal) * 100
	}
	p := m.Group.Pressure
	var symptoms []string
	var ranks []resource
	if m.Load > maxLoad || p.CPU > maxPressure {
		symptoms, ranks = append(symptoms, fmt.Sprintf("load %.1f/core, cpu pressure %.0f%%", m.Load, p.CPU)), append(ranks, cpu)
	}
	if memPct > maxMemory || p.Memory > maxPressure {
		symptoms, ranks = append(symptoms, fmt.Sprintf("memory %.0f%%, memory pressure %.0f%%", memPct, p.Memory)), append(ranks, memory)
	}
	if p.IO > maxPressure {
		symptoms, ranks = append(symptoms, fmt.Sprintf("io pressure %.0f%%", p.IO)), append(ranks, disk)
	}
	if len(symptoms) == 0 {
		r.Healthy = true
		r.Summary = fmt.Sprintf("%s: healthy (load %.1f/core, memory %.0f%%)", machine, m.Load, memPct)
		r.Fingerprint = "machine|healthy"
		return r, nil
	}
	r.Summary = fmt.Sprintf("%s: %s", machine, strings.Join(symptoms, "; "))
	base, err := s.baseline(machine, now)
	if err != nil {
		return Report{}, err
	}
	if b, ok := base["machine"]; ok && b.At.Before(m.At) {
		r.Summary += fmt.Sprintf(". Load was %.1f/core at %s", b.Load, b.At.Local().Format("15:04"))
		rows := make([]Sample, 0, len(base))
		for _, x := range base {
			rows = append(rows, x)
		}
		for _, x := range withOutside(rows, b) {
			base[x.Group.Name] = x
		}
	} else {
		base = nil // the newest minute is no baseline for itself
	}
	leaves := withOutside(latest, m)
	fingerprint := []string{"machine"}
	for _, res := range ranks {
		top := rank(leaves, res, m)
		for _, x := range top {
			r.Findings = append(r.Findings, finding(x, res, m, base))
		}
		if len(top) > 0 {
			fingerprint = append(fingerprint, res.name+":"+top[0].Group.Name)
		} else {
			fingerprint = append(fingerprint, res.name)
		}
	}
	r.Fingerprint = strings.Join(fingerprint, "|")
	return r, nil
}

// baseline returns each group's first sample since now-Baseline.
func (s Samples) baseline(machine string, now time.Time) (map[string]Sample, error) {
	rows, err := s.query(`SELECT `+sampleColumns+` FROM doctor_samples WHERE machine=?1
 AND at=(SELECT MIN(at) FROM doctor_samples WHERE machine=?1 AND grp='machine' AND at>=?2)`, machine, now.Add(-Baseline).Unix())
	if err != nil {
		return nil, err
	}
	out := map[string]Sample{}
	for _, x := range rows {
		out[x.Group.Name] = x
	}
	return out, nil
}

// withOutside returns the groups that do not hold each other (engine,
// computers and sessions) and one "outside Dyna" group for the rest of the
// machine. Agents hold the sessions, so they are left out.
func withOutside(latest []Sample, m Sample) []Sample {
	var out []Sample
	outside := m
	outside.Group.Name = OutsideGroup
	outside.Group.IO = 0
	for _, x := range latest {
		if x.Group.Name == "engine" || x.Group.Name == "computers" || x.Group.Name == "agents" {
			outside.Group.Cores = max(0, outside.Group.Cores-x.Group.Cores)
			outside.Group.Memory -= min(outside.Group.Memory, x.Group.Memory)
		}
		if x.Group.Name == "engine" || x.Group.Name == "computers" || strings.HasPrefix(x.Group.Name, "session:") {
			out = append(out, x)
		}
	}
	return append(out, outside)
}

// rank returns up to three leaves that use the most of res, each holding at
// least a tenth of the machine's use when that is measured.
func rank(leaves []Sample, res resource, m Sample) []Sample {
	var top []Sample
	for _, x := range leaves {
		v := res.of(x)
		if v <= 0 || res.share && v < res.of(m)/10 {
			continue
		}
		top = append(top, x)
	}
	sort.SliceStable(top, func(i, j int) bool { return res.of(top[i]) > res.of(top[j]) })
	return top[:min(len(top), 3)]
}

func finding(x Sample, res resource, m Sample, base map[string]Sample) Finding {
	v := res.of(x)
	f := Finding{Cause: fmt.Sprintf("%s uses %s", x.Group.Name, res.unit(v))}
	if res.share && res.of(m) > 0 {
		f.Cause += fmt.Sprintf(", %.0f%% of the machine's %s", v/res.of(m)*100, res.name)
	}
	if base == nil {
		f.Evidence = fmt.Sprintf("There are no samples from up to %s ago to compare", Baseline)
	} else if b, ok := base[x.Group.Name]; ok {
		f.Evidence = fmt.Sprintf("It used %s at %s", res.unit(res.of(b)), b.At.Local().Format("15:04"))
	} else {
		f.Evidence = fmt.Sprintf("It has no sample from %s ago, so it started since then", Baseline)
	}
	switch name := x.Group.Name; {
	case strings.HasPrefix(name, "session:"):
		f.Command = "dyna subagent show " + strings.TrimPrefix(name, "session:")
	case name == "engine":
		f.Command = "systemctl --user status dyna-engine.service"
	case name == "computers":
		f.Command = "dyna computer status"
	case res.sort != "":
		f.Command = "ps -eo pid,pcpu,rss,comm --sort=" + res.sort + " | head -n 15"
	}
	return f
}
