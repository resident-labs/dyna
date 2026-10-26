package maintenance

import (
	"errors"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/shells"
)

// A workflow worker's job names its session as Owner and the run as Run, so a
// run's scratch must match on Run.
func TestLiveShellForMatchesSessionOwnerAndWorkflowRun(t *testing.T) {
	const session, run = "ses_20261026-081252-a9074582", "wf_20261026-010101-abcdef01"
	job := func(status string) []shells.Info {
		return []shells.Info{{Meta: shells.Meta{Owner: session, Run: run}, Status: status}}
	}
	for _, tc := range []struct {
		name, id, status string
		want             bool
	}{
		{"session with a running job", session, "running", true},
		{"run with a running job", run, "running", true},
		{"run with a starting job", run, "starting", true},
		{"run whose job exited", run, "exited", false},
		{"other run", "wf_20261026-020202-abcdef02", "running", false},
	} {
		if got := liveShellFor(job(tc.status), tc.id); got != tc.want {
			t.Errorf("%s: live = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// A scratch folder is kept whenever the owner may still use it: a read that
// fails for any reason but a missing record, or a shell job that still runs.
func TestScratchOwnerKeepsWhatItCannotProveGone(t *testing.T) {
	const id = "ses_20261026-081252-a9074582"
	closed := time.Unix(1_800_000_000, 0)
	readers := func(closedAt time.Time, readErr error, live bool, shellErr error) ownerReaders {
		return ownerReaders{
			session:   func(string) (time.Time, error) { return closedAt, readErr },
			run:       func(string) (time.Time, error) { return closedAt, readErr },
			liveShell: func(string) (bool, error) { return live, shellErr },
		}
	}
	for _, tc := range []struct {
		name                string
		read                ownerReaders
		wantKnown, wantOpen bool
	}{
		{"open", readers(time.Time{}, nil, false, nil), true, true},
		{"closed", readers(closed, nil, false, nil), true, false},
		{"closed with a live shell job", readers(closed, nil, true, nil), true, true},
		{"closed, shell list unreadable", readers(closed, nil, false, errors.New("db busy")), true, true},
		{"record unreadable", readers(closed, errors.New("database slow"), false, nil), true, true},
		{"record missing", readers(closed, os.ErrNotExist, false, nil), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scratchOwnerWith(id, tc.read)
			if got.Known != tc.wantKnown || got.Open != tc.wantOpen {
				t.Fatalf("owner = %+v, want known=%t open=%t", got, tc.wantKnown, tc.wantOpen)
			}
		})
	}
}
