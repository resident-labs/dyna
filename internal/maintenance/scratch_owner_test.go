package maintenance

import (
	"errors"
	"os"
	"testing"
	"time"
)

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
