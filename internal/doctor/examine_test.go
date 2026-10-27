package doctor

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sysload"
)

func TestExamineHealthyMachineIsOneLine(t *testing.T) {
	s := Samples{DataDir: t.TempDir()}
	record(t, s, t0, 1.2, sysload.Group{Name: "machine", Cores: 2, Memory: 8 * gib})
	r, err := ExamineMachine(s, "laptop", t0.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Healthy || r.Summary != "laptop: healthy (load 1.2/core, memory 50%)" || len(r.Findings) != 0 || r.Text() != r.Summary {
		t.Fatalf("report = %+v", r)
	}
}

// busyMachine stores a quiet minute 30 minutes ago and a busy one now, where
// one session grew from 0.2 to 5 cores.
func busyMachine(t *testing.T, s Samples, now time.Time) {
	record(t, s, now.Add(-Baseline), 0.5,
		sysload.Group{Name: "machine", Cores: 2, Memory: 8 * gib},
		sysload.Group{Name: "engine", Cores: 0.5},
		sysload.Group{Name: "agents", Cores: 0.2},
		sysload.Group{Name: "session:ses_a", Cores: 0.2})
	record(t, s, now, 3,
		sysload.Group{Name: "machine", Cores: 7, Memory: 8 * gib},
		sysload.Group{Name: "engine", Cores: 0.5},
		sysload.Group{Name: "agents", Cores: 5.5},
		sysload.Group{Name: "computers"},
		sysload.Group{Name: "session:ses_a", Cores: 5},
		sysload.Group{Name: "session:ses_b", Cores: 0.5})
}

func TestExamineNamesTheGrowingConsumer(t *testing.T) {
	s := Samples{DataDir: t.TempDir()}
	busyMachine(t, s, t0)
	r, err := ExamineMachine(s, "laptop", t0)
	if err != nil {
		t.Fatal(err)
	}
	then := t0.Add(-Baseline).Local().Format("15:04")
	if r.Healthy || r.Summary != "laptop: load 3.0/core, cpu pressure 0%. Load was 0.5/core at "+then {
		t.Fatalf("summary = %q", r.Summary)
	}
	// Engine (0.5) and ses_b (0.5) hold under a tenth of 7 cores; outside
	// Dyna holds 7 - 0.5 - 5.5 = 1 core.
	want := []Finding{
		{Cause: "session:ses_a uses 5.0 cores, 71% of the machine's CPU", Evidence: "It used 0.2 cores at " + then, Command: "dyna subagent show ses_a", Kind: GroupSession},
		{Cause: "outside Dyna uses 1.0 cores, 14% of the machine's CPU", Evidence: "It used 1.3 cores at " + then, Command: "ps -eo pid,pcpu,rss,comm --sort=-pcpu | head -n 15", Kind: GroupOutside},
	}
	if len(r.Findings) != len(want) {
		t.Fatalf("findings = %+v", r.Findings)
	}
	for i := range want {
		if r.Findings[i] != want[i] {
			t.Errorf("finding %d = %+v, want %+v", i, r.Findings[i], want[i])
		}
	}
	if r.Fingerprint != "machine|CPU:session:ses_a" {
		t.Fatalf("fingerprint = %q", r.Fingerprint)
	}
	if !strings.Contains(r.Text(), "1. session:ses_a uses 5.0 cores") || !strings.Contains(r.Text(), "Next: `dyna subagent show ses_a`") {
		t.Fatalf("text = %s", r.Text())
	}
}

func TestGroupKindDrivesInclusionAndCommand(t *testing.T) {
	machine := Sample{Group: sysload.Group{Name: "machine", Cores: 10, Memory: 1000}}
	session := Sample{Group: sysload.Group{Name: "busy worker", Cores: 4}, Kind: GroupSession, SessionID: "ses_a"}
	engine := Sample{Group: sysload.Group{Name: "service", Cores: 2}, Kind: GroupEngine}
	computers := Sample{Group: sysload.Group{Name: "desktops", Cores: 1}, Kind: GroupComputers}
	agents := Sample{Group: sysload.Group{Name: "agents", Cores: 3}}
	leaves := withOutside([]Sample{machine, session, engine, computers, agents}, machine)
	byKind := map[GroupKind]Sample{}
	for _, x := range leaves {
		byKind[x.Kind] = x
	}
	if byKind[GroupSession].SessionID != "ses_a" || byKind[GroupEngine].Kind != GroupEngine || byKind[GroupComputers].Kind != GroupComputers {
		t.Fatalf("leaves = %+v", leaves)
	}
	if byKind[GroupOutside].Group.Cores != 4 || byKind[GroupOutside].Group.Name != OutsideGroup {
		t.Fatalf("outside = %+v", byKind[GroupOutside])
	}
	if cmd := finding(byKind[GroupSession], cpu, machine, nil).Command; cmd != "dyna subagent show ses_a" {
		t.Fatalf("session command = %q", cmd)
	}
	if cmd := finding(byKind[GroupEngine], cpu, machine, nil).Command; cmd != "systemctl --user status dyna-engine.service" {
		t.Fatalf("engine command = %q", cmd)
	}
	if cmd := finding(byKind[GroupComputers], cpu, machine, nil).Command; cmd != "dyna computer status" {
		t.Fatalf("computers command = %q", cmd)
	}
	if f := finding(byKind[GroupOutside], cpu, machine, nil); f.Kind != GroupOutside || !strings.HasPrefix(f.Command, "ps ") {
		t.Fatalf("outside finding = %+v", f)
	}
}

func TestExamineNeedsFreshSamples(t *testing.T) {
	s := Samples{DataDir: t.TempDir()}
	if _, err := ExamineMachine(s, "laptop", t0); err == nil {
		t.Fatal("no samples passed")
	}
	record(t, s, t0, 1, sysload.Group{Name: "machine", Memory: gib})
	if _, err := ExamineMachine(s, "laptop", t0.Add(10*time.Minute)); err == nil || !strings.Contains(err.Error(), "dyna-engine.service") {
		t.Fatalf("stale samples: %v", err)
	}
}
