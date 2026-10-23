package doctor

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunFalseConditionStoresNothing(t *testing.T) {
	dir := t.TempDir()
	busyMachine(t, Samples{DataDir: dir}, t0)
	posted := 0
	res, err := Run{DataDir: dir, Machine: "laptop", Target: "machine", If: "memory>85%", Now: t0, Post: func(Report) error { posted++; return nil }}.Do()
	if err != nil || res.Ran || posted != 0 {
		t.Fatalf("result = %+v, %v, posted %d", res, err, posted)
	}
	if list, err := (Reports{DataDir: dir}).List(10); err != nil || len(list) != 0 {
		t.Fatalf("reports = %+v, %v", list, err)
	}
}

func TestRunPostsOnceWithinTheCooldown(t *testing.T) {
	dir := t.TempDir()
	busyMachine(t, Samples{DataDir: dir}, t0)
	var posted []Report
	run := Run{DataDir: dir, Machine: "laptop", Target: "machine", Trigger: "hook schedule", If: "load>2", Cooldown: 30 * time.Minute, Now: t0,
		Post: func(r Report) error { posted = append(posted, r); return nil }}
	first, err := run.Do()
	if err != nil || !first.Ran || first.Repeat || first.Report.ID == 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if !strings.HasSuffix(first.Report.Summary, ". Condition met: load 3.0/core") || first.Report.Trigger != "hook schedule" || first.Report.Condition != "load>2" {
		t.Fatalf("report = %+v", first.Report)
	}
	run.Now = t0.Add(time.Minute)
	second, err := run.Do()
	if err != nil || !second.Repeat || second.Report.ID != first.Report.ID {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if len(posted) != 1 || posted[0].ID != first.Report.ID {
		t.Fatalf("posted = %+v", posted)
	}
	got, found, err := (Reports{DataDir: dir}).Get(first.Report.ID)
	if err != nil || !found || got.Fingerprint != first.Report.Fingerprint || len(got.Findings) != len(first.Report.Findings) {
		t.Fatalf("stored = %+v, %v, %v", got, found, err)
	}
}

func TestRunRejectsUnknownTarget(t *testing.T) {
	if _, err := (Run{DataDir: t.TempDir(), Target: "session:ses_a", Now: t0}).Do(); err == nil {
		t.Fatal("unknown target passed")
	}
}

func TestCooldownIsPerMachine(t *testing.T) {
	dir := t.TempDir()
	busyMachine(t, Samples{DataDir: dir}, t0)
	// A report from another machine must not suppress this machine.
	_, err := (Reports{DataDir: dir}).Save(Report{At: t0, Machine: "desktop", Target: "machine", Fingerprint: "machine|CPU:session:ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (Run{DataDir: dir, Machine: "laptop", Target: "machine", Now: t0, Cooldown: time.Hour}).Do()
	if err != nil || res.Repeat || res.Report.Machine != "laptop" {
		t.Fatalf("result=%+v, err=%v", res, err)
	}
}

func TestRunRetriesFailedDelivery(t *testing.T) {
	dir := t.TempDir()
	busyMachine(t, Samples{DataDir: dir}, t0)
	var attempts []int64
	run := Run{DataDir: dir, Machine: "laptop", Target: "machine", Now: t0, Cooldown: time.Hour,
		Post: func(r Report) error {
			attempts = append(attempts, r.ID)
			if len(attempts) == 1 {
				return fmt.Errorf("temporary failure")
			}
			return nil
		}}
	first, err := run.Do()
	if err == nil {
		t.Fatal("first post should fail")
	}
	second, err := run.Do()
	if err != nil || second.Report.ID != first.Report.ID || len(attempts) != 2 {
		t.Fatalf("retry=%+v attempts=%v err=%v", second, attempts, err)
	}
	if _, err = run.Do(); err != nil || len(attempts) != 2 {
		t.Fatalf("delivered report posted again: %v, %v", attempts, err)
	}
}
