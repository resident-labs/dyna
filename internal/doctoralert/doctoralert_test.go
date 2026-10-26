package doctoralert

import (
	"dyna-agent/internal/sysload"
	"io"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func fake(t *testing.T, rooms []RoomUse) (*Notifier, *[]Note, *time.Time) {
	now := t0
	var sent []Note
	return &Notifier{
		DataDir: t.TempDir(),
		Now:     func() time.Time { return now },
		Use:     func() ([]RoomUse, error) { return rooms, nil },
		Send:    func(n Note) error { sent = append(sent, n); return nil },
	}, &sent, &now
}

func TestEachRoomWithAgentsHearsOncePerIntervalThenRecovery(t *testing.T) {
	n, sent, now := fake(t, []RoomUse{{RoomID: "a", Room: "a", Running: 2, Idle: 29}, {RoomID: "b", Room: "b", Running: 4}})
	rep := doctor.Report{Machine: "laptop"}
	if err := n.Alert(rep); err != nil || len(*sent) != 2 {
		t.Fatalf("first alert sent %d, %v", len(*sent), err)
	}
	first := (*sent)[0]
	if first.Total != 35 || !strings.Contains(first.Advice(), "this room runs 31 of 35 agent sessions") || !strings.Contains(first.Advice(), "Close the 29 idle") {
		t.Fatalf("advice = %q total %d", first.Advice(), first.Total)
	}
	if got := (*sent)[1].Advice(); !strings.Contains(got, "Pause or stop 2 of the 4 running") {
		t.Fatalf("running-only advice = %q", got)
	}
	*now = t0.Add(Interval - time.Minute)
	if err := n.Alert(rep); err != nil || len(*sent) != 2 {
		t.Fatalf("alert inside the interval sent %d, %v", len(*sent), err)
	}
	*now = t0.Add(Interval)
	if err := n.Alert(rep); err != nil || len(*sent) != 4 {
		t.Fatalf("alert after the interval sent %d, %v", len(*sent), err)
	}
	if err := n.Recover("laptop"); err != nil || len(*sent) != 6 || !(*sent)[5].Recovered {
		t.Fatalf("recovery sent %d, %v", len(*sent), err)
	}
	if err := n.Recover("laptop"); err != nil || len(*sent) != 6 {
		t.Fatalf("second recovery sent %d, %v", len(*sent), err)
	}
	if err := n.Alert(rep); err != nil || len(*sent) != 8 {
		t.Fatalf("a new overload must alert at once, sent %d, %v", len(*sent), err)
	}
}

func TestCountSkipsClosedDormantAndRoomless(t *testing.T) {
	mk := func(room, status string) subagent.Info {
		return subagent.Info{Meta: subagent.Meta{RoomID: room, Room: room}, Status: status}
	}
	got := Count([]subagent.Info{mk("a", subagent.StatusRunning), mk("a", subagent.StatusIdle), mk("a", subagent.StatusDormant), mk("b", subagent.StatusClosed), mk("", subagent.StatusIdle)})
	if len(got) != 1 || got[0].Running != 1 || got[0].Idle != 1 {
		t.Fatalf("count = %+v", got)
	}
}

func TestPostNoteWakesRootForAnOverloadButNotForRecovery(t *testing.T) {
	store := &room.Store{Root: t.TempDir()}
	rec, err := store.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	use := RoomUse{RoomID: rec.ID, Room: rec.Name, Running: 1, Idle: 3}
	rep := doctor.Report{ID: 7, Machine: "laptop", Summary: "laptop: load 3.0/core", Findings: []doctor.Finding{{Cause: "session:ses_a uses 5.0 cores", Command: "dyna subagent show ses_a"}}}
	if err := PostNote(store, Note{Room: use, Machine: "laptop", Total: 9, Report: rep}); err != nil {
		t.Fatal(err)
	}
	if err := PostNote(store, Note{Room: use, Machine: "laptop", Recovered: true}); err != nil {
		t.Fatal(err)
	}
	posts, err := store.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	alert, ok := posts[0].Alert()
	if !ok || !posts[0].Wake || alert.NextCommand != "dyna subagent show ses_a" || !strings.Contains(alert.Detail, "this room runs 4 of 9") {
		t.Fatalf("overload post = %+v %+v", posts[0], alert)
	}
	if posts[1].Wake {
		t.Fatalf("recovery woke the root: %+v", posts[1])
	}
}

// An overload that returns inside the report cooldown must alert again, and a
// pass with no fresh samples must not announce recovery.
func TestOverloadRecoveryThenSameOverloadAlertsAgain(t *testing.T) {
	n, sent, now := fake(t, []RoomUse{{RoomID: "a", Room: "a", Running: 2}})
	samples := doctor.Samples{DataDir: n.DataDir}
	load := func(at time.Time, perCore float64) {
		l := sysload.Load{At: at, CPUs: 8, Load: [3]float64{perCore * 8}, Memory: sysload.Memory{Total: 16 << 30}, Groups: []sysload.Group{{Name: "machine", Cores: perCore * 8, Memory: 8 << 30}}}
		if err := samples.Record("laptop", l); err != nil {
			t.Fatal(err)
		}
	}
	pass := func() {
		t.Helper()
		err := doctor.Pass{DataDir: n.DataDir, Machines: []string{"laptop"}, If: "load>2", Cooldown: doctor.DefaultCooldown, Now: *now, Alert: n.Alert, Clear: n.Recover}.Do(io.Discard)
		if err != nil {
			t.Fatal(err)
		}
	}
	load(*now, 3)
	pass() // overload: alert
	*now = now.Add(time.Minute)
	load(*now, 0.5)
	pass() // fresh and healthy: recovery
	if len(*sent) != 2 || !(*sent)[1].Recovered {
		t.Fatalf("sent %d after recovery, want alert then recovery", len(*sent))
	}
	*now = now.Add(time.Minute)
	load(*now, 3)
	pass() // the same overload, inside the report cooldown, alerts again
	if len(*sent) != 3 || (*sent)[2].Recovered {
		t.Fatalf("sent %d after the overload returned, want a new alert", len(*sent))
	}
	*now = now.Add(time.Hour) // the samples are stale now
	pass()
	if len(*sent) != 3 {
		t.Fatalf("stale samples announced recovery: sent %d", len(*sent))
	}
}

// After a gap in recording, one fresh overloaded sample does not cover the
// five-minute load window. That is unknown, not recovery, so the alert stays.
func TestSampleGapDoesNotAnnounceRecoveryWhileOverloaded(t *testing.T) {
	n, sent, now := fake(t, []RoomUse{{RoomID: "a", Room: "a", Running: 2}})
	samples := doctor.Samples{DataDir: n.DataDir}
	load := func(at time.Time) {
		l := sysload.Load{At: at, CPUs: 8, Load: [3]float64{24}, Memory: sysload.Memory{Total: 16 << 30}, Groups: []sysload.Group{{Name: "machine", Cores: 24, Memory: 8 << 30}}}
		if err := samples.Record("laptop", l); err != nil {
			t.Fatal(err)
		}
	}
	pass := func() {
		t.Helper()
		err := doctor.Pass{DataDir: n.DataDir, Machines: []string{"laptop"}, If: "load>2/core for 5m or memory>85% or pressure.memory>20%", Cooldown: doctor.DefaultCooldown, Now: *now, Alert: n.Alert, Clear: n.Recover}.Do(io.Discard)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i <= 6; i++ {
		*now = t0.Add(time.Duration(i) * time.Minute)
		load(*now)
	}
	pass()
	if len(*sent) != 1 || (*sent)[0].Recovered {
		t.Fatalf("sent %d, want the first alert", len(*sent))
	}
	*now = t0.Add(20 * time.Minute) // recording resumed after a 14 minute gap
	load(*now)
	pass()
	if len(*sent) != 1 {
		t.Fatalf("a sample gap announced recovery or re-alerted: sent %d", len(*sent))
	}
}

func TestAdviceNamesTheTurnsThatWaitForASlot(t *testing.T) {
	infos := []subagent.Info{
		{Meta: subagent.Meta{RoomID: "a", Room: "a"}, Status: subagent.StatusQueued},
		{Meta: subagent.Meta{RoomID: "a", Room: "a"}, Status: subagent.StatusRunning},
		{Meta: subagent.Meta{RoomID: "a", Room: "a"}, Status: subagent.StatusIdle},
	}
	use := Count(infos)
	if len(use) != 1 || use[0].Queued != 1 || use[0].Running != 2 {
		t.Fatalf("count = %+v", use)
	}
	advice := Note{Room: use[0], Machine: "laptop", Total: 9}.Advice()
	if !strings.Contains(advice, "1 of its turns wait for a free slot, so stop starting new agents") {
		t.Fatalf("advice = %q", advice)
	}
}
