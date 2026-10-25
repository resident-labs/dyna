package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func testProfile() profile.Profile {
	return profile.Profile{Name: "worker", Harness: profile.HarnessPi, Model: "test-model"}
}

func observation(at int) Observation {
	return Observation{At: time.Unix(int64(at), 0).UTC(), Successful: true,
		StdoutSeen: true, RecognizedRecords: 1, TextSeen: true}
}

func TestOutputNotUnderstoodAndRecovery(t *testing.T) {
	s := New(t.TempDir())
	p := testProfile()
	o := observation(1)
	o.RecognizedRecords = 0
	v := recordTurn(t, s, p, "v1", o)
	if v.Status != OutputNotUnderstood || v.Observed.RecognizedOutput || v.StorageError != "" {
		t.Fatalf("unrecognized stdout: %+v", v)
	}
	o = observation(2)
	o.TextSeen = false // A normal turn needs no tool, text, or positive usage.
	v = recordTurn(t, s, p, "v1", o)
	if v.Status != Healthy || v.LastHealthyVersion != "v1" || v.Observed.ToolSeen {
		t.Fatalf("recovery: %+v", v)
	}
	if !v.Declared.ToolEvents || v.LastCheckedAt != o.At || v.Observed.LastTurnAt != o.At {
		t.Fatalf("missing contract or times: %+v", v)
	}
}

func TestVersionDriftAndProbeRecovery(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	recordTurn(t, s, p, "v1", observation(1))
	if v := s.View(p, "v2"); v.Status != UnverifiedSinceUpdate || v.LastHealthyVersion != "v1" {
		t.Fatalf("version drift: %+v", v)
	}
	v := recordProbe(t, s, p, "v2", observation(2), false)
	if v.Status != UnverifiedSinceUpdate || v.LastHealthyVersion != "v1" {
		t.Fatalf("failed probe cleared drift: %+v", v)
	}
	v = recordProbe(t, s, p, "v2", observation(3), true)
	if v.Status != Healthy || v.LastHealthyVersion != "v2" || !v.Observed.Probe || !v.Observed.LastTurnAt.Equal(time.Unix(1, 0)) {
		t.Fatalf("probe recovery: %+v", v)
	}
	v = recordProbe(t, s, p, "v2", observation(4), false)
	if v.Status != Unhealthy {
		t.Fatalf("strict probe: %+v", v)
	}
	v = recordTurn(t, s, p, "v3", observation(5))
	if v.Status != Healthy || v.LastHealthyVersion != "v3" {
		t.Fatalf("ordinary turn recovery: %+v", v)
	}
}

func TestProbeCannotBlessUnrecognizedOrUnsuccessfulOutput(t *testing.T) {
	for _, missing := range []string{"records", "success"} {
		t.Run(missing, func(t *testing.T) {
			o := observation(1)
			if missing == "records" {
				o.RecognizedRecords = 0
			} else {
				o.Successful = false
			}
			v := recordProbe(t, New(t.TempDir()), testProfile(), "v1", o, true)
			if v.Status == Healthy || v.LastHealthyVersion != "" {
				t.Fatalf("invalid probe: %+v", v)
			}
		})
	}
}

func TestFingerprintInvalidation(t *testing.T) {
	for _, field := range []string{"harness", "model", "extraArgs", "output"} {
		t.Run(field, func(t *testing.T) {
			s, p := New(t.TempDir()), testProfile()
			recordTurn(t, s, p, "v1", observation(1))
			switch field {
			case "harness":
				p.Harness = profile.HarnessClaudeCode
			case "model":
				p.Model = "different-model"
			case "extraArgs":
				p.ExtraArgs = []string{"--different"}
			case "output":
				p.Output = &workeroutput.Declaration{Dialect: workeroutput.DialectText}
			}
			v := s.View(p, "v1")
			if v.Status != UnverifiedSinceEdit || v.Observed != nil || v.LastHealthyVersion != "" {
				t.Fatalf("stale evidence exposed: %+v", v)
			}
			v = recordTurn(t, s, p, "v1", observation(2))
			if v.Status != Healthy {
				t.Fatalf("edited profile recovery: %+v", v)
			}
		})
	}
}

func TestReloadAndPrivateStorage(t *testing.T) {
	dir, p := t.TempDir(), testProfile()
	p.Name = "../../worker/with/slashes"
	p.Env = map[string]string{"SECRET": "do-not-store-env"}
	p.ExtraArgs = []string{"do-not-store-args"}
	s := New(dir)
	o := observation(1)
	o.ToolSeen, o.PositiveUsageSeen = true, true
	r := record{Fingerprint: Fingerprint(p), Version: "v1", LastHealthyVersion: "v1", LastHealthyAt: o.At,
		Observed: Observed{Observation: o, LastTurnAt: o.At, RecognizedOutput: true}}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.path(p.Name)
	if err != nil || filepath.Dir(path) != dir || filepath.Base(path) != key(p.Name)+".json" {
		t.Fatalf("unsafe path: %s, %v", path, err)
	}
	// Check durable storage without RecordTurn's optional work deadline.
	if err := s.saveRecord(t.Context(), db, p.Name, &r, func() {}); err != nil {
		t.Fatal("persist private health record:", err)
	}
	v := New(dir).View(p, "v1")
	if v.Status != Healthy || v.StorageError != "" || v.Observed == nil || !v.Observed.PositiveUsageSeen || !v.Observed.ToolSeen {
		t.Fatalf("reload: %+v", v)
	}
	var b []byte
	if err := db.QueryRow("SELECT record FROM harness_health WHERE profile = ?", key(p.Name)).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) || strings.Contains(string(b), "do-not-store") {
		t.Fatalf("unsafe health record: %s", b)
	}
	before := Fingerprint(p)
	p.Env["SECRET"] = "another-secret"
	if Fingerprint(p) != before {
		t.Fatal("environment entered fingerprint")
	}
	other := p
	other.Name = "other-profile"
	if got := s.View(other, "v1"); got.Status != Unknown {
		t.Fatalf("profiles share health: %+v", got)
	}
}

func TestDefaultCacheAndStorageErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	s := New("")
	v := recordTurn(t, s, testProfile(), "v1", observation(1))
	if v.Status != Healthy || v.StorageError != "" {
		t.Fatalf("default cache: %+v", v)
	}
	path, _ := s.path(testProfile().Name)
	if filepath.Dir(path) != filepath.Join(dir, "dyna", "profile-health") {
		t.Fatalf("default path: %s", path)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s = New(file)
	v = s.RecordTurn(testProfile(), "v1", observation(1))
	if v.Status != Healthy || v.StorageError == "" {
		t.Fatalf("write failure lost evidence: %+v", v)
	}
	v = s.View(testProfile(), "v1")
	if v.Status != Unknown || v.StorageError == "" {
		t.Fatalf("read failure: %+v", v)
	}
}

func TestParallelWritersPreserveLatestAndHealthyEvidence(t *testing.T) {
	dir, p := t.TempDir(), testProfile()
	ctx := t.Context()
	writeErrors := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 1; i <= 12; i++ {
		wg.Go(func() {
			o := observation(i)
			if i == 12 {
				o.Successful = false
			}
			_, err := persistForTest(ctx, New(dir), p, "v1", o, false, false)
			writeErrors <- err
		})
	}
	wg.Wait()
	close(writeErrors)
	for err := range writeErrors {
		if err != nil {
			t.Errorf("parallel writer: %v", err)
		}
	}
	if t.Failed() {
		return
	}
	v := New(dir).View(p, "v1")
	if v.Status != Unhealthy || v.LastHealthyVersion != "v1" || !v.LastCheckedAt.Equal(observation(12).At) {
		t.Fatalf("lost concurrent evidence: %+v", v)
	}
	if v.Observed.LastTurnAt != observation(12).At {
		t.Fatalf("last turn regressed: %+v", v.Observed)
	}
}

// Malformed retained evidence is skipped. A later observation creates a SQL row.
func TestCorruptCacheIsRepaired(t *testing.T) {
	for _, data := range []string{"{", `{"observed":{"recognizedRecords":"wrong-type"}}`} {
		s, p := New(t.TempDir()), testProfile()
		path, _ := s.path(p.Name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if v := s.View(p, "v1"); v.StorageError != "" || v.Status != Unknown {
			t.Fatalf("corrupt cache: %+v", v)
		}
		if v := recordTurn(t, s, p, "v1", observation(1)); v.StorageError != "" || v.Status != Healthy {
			t.Fatalf("cache recovery: %+v", v)
		}
	}
}

func TestProgressCannotBlessVersionOrCompleteTurn(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	recordTurn(t, s, p, "v1", observation(1))
	o := observation(2)
	v := recordProgress(t, s, p, "v2", o)
	if v.Status != UnverifiedSinceUpdate || v.LastHealthyVersion != "v1" || !v.Observed.Running || v.Observed.LastTurnAt != observation(1).At {
		t.Fatalf("progress changed completed health: %#v", v)
	}
	o.RecognizedRecords = 0
	v = recordProgress(t, s, p, "v2", o)
	if v.Status != OutputNotUnderstood || !v.Observed.Running {
		t.Fatalf("running output mismatch: %#v", v)
	}
	v = recordTurn(t, s, p, "v2", observation(3))
	if v.Status != Healthy || v.Observed.Running || v.LastHealthyVersion != "v2" {
		t.Fatalf("completion did not replace progress: %#v", v)
	}
}
