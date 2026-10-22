package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

// Persistence assertions use the same observation and commit/copy code as
// RecordTurn, with the test context instead of optional production deadlines.
func recordForTest(t *testing.T, s *Service, p profile.Profile, version string, o Observation, probe, passed bool) View {
	t.Helper()
	v, err := persistForTest(t.Context(), s, p, version, o, probe, passed)
	if err != nil {
		t.Fatalf("persist health observation: %v", err)
	}
	return v
}

// Worker goroutines return errors so the test goroutine can report failures.
func persistForTest(ctx context.Context, s *Service, p profile.Profile, version string, o Observation, probe, passed bool) (View, error) {
	path, err := s.path(p.Name)
	if err != nil {
		return View{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return View{}, err
	}
	db, err := s.db()
	if err != nil {
		return View{}, err
	}
	unlock, err := store.LockContext(ctx, path+".lock")
	if err != nil {
		return View{}, err
	}
	defer unlock()
	v := recordObservation(p, version, o, probe, passed, func(name string, r *record, apply func()) error {
		return s.saveRecord(ctx, db, name, path, r, apply)
	})
	if v.StorageError != "" {
		return v, errors.New(v.StorageError)
	}
	return v, nil
}

func recordTurn(t *testing.T, s *Service, p profile.Profile, version string, o Observation) View {
	t.Helper()
	return recordForTest(t, s, p, version, o, false, false)
}

func recordProbe(t *testing.T, s *Service, p profile.Profile, version string, o Observation, passed bool) View {
	t.Helper()
	return recordForTest(t, s, p, version, o, true, passed)
}

func recordProgress(t *testing.T, s *Service, p profile.Profile, version string, o Observation) View {
	t.Helper()
	o.Running = true
	return recordForTest(t, s, p, version, o, false, false)
}
