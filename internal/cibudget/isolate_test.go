package cibudget

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsolateReplacesLoadedTimeWithAloneTime(t *testing.T) {
	r := Report{Tests: []Test{
		{Package: "p", Name: "TestFast", Duration: 100 * time.Millisecond},
		{Package: "p", Name: "TestLoaded", Duration: 4 * time.Second},
		{Package: "p", Name: "TestReallySlow", Duration: 3 * time.Second},
		{Package: "p", Name: "TestBroken", Duration: 2 * time.Second},
	}}
	var ran []string
	alone := func(_ context.Context, test Test) (time.Duration, error) {
		ran = append(ran, test.Name)
		switch test.Name {
		case "TestLoaded":
			return 200 * time.Millisecond, nil
		case "TestBroken":
			return 0, errors.New("exit status 1")
		}
		return 3500 * time.Millisecond, nil
	}
	var out strings.Builder
	r.Isolate(context.Background(), time.Second, alone, &out)
	if strings.Join(ran, ",") != "TestLoaded,TestReallySlow,TestBroken" {
		t.Fatalf("isolated runs = %v", ran)
	}
	got := map[string]time.Duration{}
	for _, test := range r.Tests {
		got[test.Name] = test.Duration
	}
	want := map[string]time.Duration{"TestFast": 100 * time.Millisecond, "TestLoaded": 200 * time.Millisecond, "TestReallySlow": 3 * time.Second, "TestBroken": 2 * time.Second}
	for name, d := range want {
		if got[name] != d {
			t.Errorf("%s = %s, want %s", name, got[name], d)
		}
	}
	if !strings.Contains(out.String(), "ISOLATED p/TestLoaded: 4s in the full run, 200ms alone") {
		t.Fatalf("output lacks both times:\n%s", out.String())
	}
	if !r.Write(&strings.Builder{}, time.Second, map[string]bool{"p/TestReallySlow": true, "p/TestBroken": true}) {
		t.Fatal("loaded test still over budget after isolation")
	}
}

func TestIsolateStopsWhenContextEndsAndKeepsReportedTimes(t *testing.T) {
	r := Report{Tests: []Test{{Package: "p", Name: "TestA", Duration: 4 * time.Second}, {Package: "p", Name: "TestB", Duration: 3 * time.Second}}}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	alone := func(context.Context, Test) (time.Duration, error) {
		calls++
		cancel()
		return 100 * time.Millisecond, nil
	}
	var out strings.Builder
	r.Isolate(ctx, time.Second, alone, &out)
	if calls != 1 || r.Tests[0].Duration != 100*time.Millisecond || r.Tests[1].Duration != 3*time.Second {
		t.Fatalf("calls = %d, tests = %+v", calls, r.Tests)
	}
	if !strings.Contains(out.String(), "Isolated runs stopped: context canceled") {
		t.Fatalf("output = %q", out.String())
	}
}
