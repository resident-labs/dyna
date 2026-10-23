package doctor

import (
	"testing"
	"time"

	"dyna-agent/internal/sysload"
)

const gib = 1 << 30

// record stores one minute on a machine with 8 CPUs and 16 GiB.
func record(t *testing.T, s Samples, at time.Time, loadPerCore float64, groups ...sysload.Group) {
	t.Helper()
	l := sysload.Load{At: at, CPUs: 8, Load: [3]float64{loadPerCore * 8}, Memory: sysload.Memory{Total: 16 * gib}, Groups: groups}
	if err := s.Record("laptop", l); err != nil {
		t.Fatal(err)
	}
}

func TestParseConditionErrors(t *testing.T) {
	for _, text := range []string{"", "load", "load>", ">2", "load>2 and", "load>2 for", "load>2 for 10s", "load>2 for soon", "foo>1", "cores>1", "load>abc", "load>2 xor memory>1", "memory.agents>2T"} {
		if _, err := ParseCondition(text); err == nil {
			t.Errorf("ParseCondition(%q) passed", text)
		}
	}
}

func TestConditionHolds(t *testing.T) {
	s := Samples{DataDir: t.TempDir()}
	for i := range 6 {
		perCore := 3.0
		if i == 2 {
			perCore = 1.5
		}
		record(t, s, t0.Add(time.Duration(i)*time.Minute), perCore,
			sysload.Group{Name: "machine", Cores: 6, Memory: 15 * gib, Pressure: sysload.Pressure{IO: 12}},
			sysload.Group{Name: "engine", Cores: 1},
			sysload.Group{Name: "agents", Memory: 2 * gib})
	}
	now := t0.Add(5 * time.Minute)
	read := func(group string, since time.Time) ([]Sample, error) { return s.Window("laptop", group, since) }
	for _, tc := range []struct {
		text    string
		at      time.Time
		want    bool
		reading string
	}{
		{"load>2", now, true, "load 3.0/core"},
		{"load>2/core for 2m", now, true, "load 3.0/core for 2m0s"},
		{"load>2 for 5m", now, false, ""},  // minute 2 was 1.5
		{"load>1 for 10m", now, false, ""}, // only 5 minutes are stored
		{"load>1 for 5m", now, true, "load 3.0/core for 5m0s"},
		{"load<2", now, false, ""},
		{"memory>85%", now, true, "memory 94%"},
		{"memory>85% and load<1", now, false, ""},
		{"load<1 or memory>85% and pressure.io>10", now, true, "memory 94%, pressure.io 12%"},
		{"memory.agents>1G", now, true, "agents memory 2.0 GiB"},
		{"cores.engine>0.5", now, true, "engine 1.0 cores"},
		{"cores.computers>0.5", now, false, ""},         // no such group stored
		{"load>2", now.Add(5 * time.Minute), false, ""}, // the newest sample is stale
	} {
		c, err := ParseCondition(tc.text)
		if err != nil {
			t.Fatalf("%s: %v", tc.text, err)
		}
		got, reading, err := c.Holds(read, tc.at)
		if err != nil || got != tc.want || reading != tc.reading {
			t.Errorf("%s = %v %q %v, want %v %q", tc.text, got, reading, err, tc.want, tc.reading)
		}
	}
}

func TestTimedConditionRequiresContinuousFreshSamples(t *testing.T) {
	c, err := ParseCondition("load>2 for 5m")
	if err != nil {
		t.Fatal(err)
	}
	for _, offsets := range [][]time.Duration{
		{-5 * time.Minute},    // A stopped recorder does not prove sustained load.
		{-5 * time.Minute, 0}, // Neither does a gap in the record.
	} {
		read := func(string, time.Time) ([]Sample, error) {
			var samples []Sample
			for _, offset := range offsets {
				samples = append(samples, Sample{At: t0.Add(offset), Load: 3})
			}
			return samples, nil
		}
		if ok, _, err := c.Holds(read, t0); err != nil || ok {
			t.Fatalf("offsets %v: held=%v, err=%v", offsets, ok, err)
		}
	}
}
