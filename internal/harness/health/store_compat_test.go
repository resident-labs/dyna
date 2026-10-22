package health

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

func TestStorageGoldenBytes(t *testing.T) {
	s := New(t.TempDir())
	p := profile.Profile{Name: "golden", Harness: profile.HarnessMock}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	o := Observation{At: at, Successful: true, RecognizedRecords: 1}
	v := recordTurn(t, s, p, "built-in", o)
	if v.StorageError != "" {
		t.Fatal(v.StorageError)
	}
	path, err := s.path(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"fingerprint":"` + Fingerprint(p) + `","version":"built-in","lastHealthyVersion":"built-in","lastHealthyAt":"2026-01-02T03:04:05Z","observed":{"running":false,"at":"2026-01-02T03:04:05Z","successful":true,"stdoutSeen":false,"recognizedRecords":1,"textSeen":false,"toolSeen":false,"positiveUsageSeen":false,"lastTurnAt":"2026-01-02T03:04:05Z","recognizedOutput":true,"probe":false,"probePassed":false}}`
	if string(got) != want {
		t.Fatalf("health bytes = %q, want %q", got, want)
	}
	old, err := json.Marshal(record{Fingerprint: Fingerprint(p), Version: "built-in", LastHealthyVersion: "built-in", LastHealthyAt: at, Observed: Observed{Observation: o, LastTurnAt: at, RecognizedOutput: true}})
	if err != nil || string(got) != string(old) {
		t.Fatal("old health encoder differs")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}
