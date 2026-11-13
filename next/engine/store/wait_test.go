package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// writerAdmissionContext observes Write's context select, which runs after it
// acquires s.mu. Callers using this must reach Write without consulting Done first.
type writerAdmissionContext struct {
	context.Context
	admitted chan struct{}
	once     sync.Once
}

func (c *writerAdmissionContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.admitted) })
	return c.Context.Done()
}

func awaitStoreSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("store operation did not reach its synchronization boundary")
	}
}

// Capture the ownership capability under Write, before releasing its keeper.
func volumeRetirement(t *testing.T, s *Store, id contract.BoxID, host contract.HostName) <-chan struct{} {
	t.Helper()
	var done <-chan struct{}
	if err := s.Write(context.Background(), func(*sql.Tx) error {
		for claim := range s.volumeRemovals[id] {
			if claim.host == host {
				if done != nil {
					t.Error("multiple volume claims for host")
				}
				done = claim.done
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if done == nil {
		t.Fatal("missing volume claim")
	}
	return done
}
