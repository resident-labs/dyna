package doctor

import (
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/sysload"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func load(at time.Time, groups int) sysload.Load {
	l := sysload.Load{At: at, CPUs: 8, Load: [3]float64{12, 0, 0}, Memory: sysload.Memory{Total: 16 << 30, Used: 9 << 30}}
	l.Groups = append(l.Groups, sysload.Group{Name: "machine", Cores: 3.5, Memory: 9 << 30, Pressure: sysload.Pressure{CPU: 20, Memory: 1, IO: 2}})
	for i := 1; i < groups; i++ {
		l.Groups = append(l.Groups, sysload.Group{Name: fmt.Sprintf("session:ses_%d", i), Cores: 0.5, Memory: 1 << 20, IO: 100})
	}
	return l
}

func TestRecordAndWindow(t *testing.T) {
	s := Samples{DataDir: t.TempDir()}
	for i := range 3 {
		if err := s.Record("laptop", load(t0.Add(time.Duration(i)*time.Minute), 2)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Window("laptop", "machine", t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("window = %+v", got)
	}
	m := got[0]
	if m.Load != 1.5 || m.MemoryTotal != 16<<30 || m.Group.Cores != 3.5 || m.Group.Pressure.CPU != 20 {
		t.Fatalf("machine sample = %+v", m)
	}
	ses, err := s.Window("laptop", "session:ses_1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ses) != 3 || ses[0].Load != 0 || ses[0].MemoryTotal != 0 || ses[0].Group.IO != 100 {
		t.Fatalf("session samples = %+v", ses)
	}
	other, err := s.Window("desktop", "machine", t0)
	if err != nil || len(other) != 0 {
		t.Fatalf("another machine's window = %+v, %v", other, err)
	}
}

func TestRecordDropsSamplesOlderThanKeep(t *testing.T) {
	s := Samples{DataDir: t.TempDir()}
	for _, at := range []time.Time{t0, t0.Add(time.Hour), t0.Add(Keep + 30*time.Minute)} {
		if err := s.Record("laptop", load(at, 1)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Window("laptop", "machine", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("samples after pruning = %+v", got)
	}
}

// fill stores a full day of minutes for groups groups, the most Keep allows.
func fill(b *testing.B, s Samples, groups int) {
	b.Helper()
	for i := range int(Keep / time.Minute) {
		if err := s.Record("laptop", load(t0.Add(time.Duration(i)*time.Minute), groups)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWindowFiveMinutes is the read a one-minute condition check makes:
// five minutes of the machine group from a full day of 40 groups.
func BenchmarkWindowFiveMinutes(b *testing.B) {
	s := Samples{DataDir: b.TempDir()}
	fill(b, s, 40)
	end := t0.Add(Keep - time.Minute)
	b.ResetTimer()
	for b.Loop() {
		got, err := s.Window("laptop", "machine", end.Add(-5*time.Minute))
		if err != nil || len(got) != 6 {
			b.Fatalf("window = %d, %v", len(got), err)
		}
	}
}

// BenchmarkRecord is the engine's write each minute: 40 groups and a prune.
func BenchmarkRecord(b *testing.B) {
	s := Samples{DataDir: b.TempDir()}
	fill(b, s, 40)
	at := t0.Add(Keep)
	b.ResetTimer()
	for b.Loop() {
		at = at.Add(time.Minute)
		if err := s.Record("laptop", load(at, 40)); err != nil {
			b.Fatal(err)
		}
	}
}
