package questions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database/databasetest"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
)

const questionSession = "ses_20261017-120000-abcdef01"

func lifecycleStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: questionRoot(t)}
}

func writeQuestionSession(t *testing.T, s *Store, data string) {
	t.Helper()
	recordtest.WriteRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), questionSession, []byte(data))
}

func TestQuestionReadersWaitForWriter(t *testing.T) {
	for _, method := range []string{"ReadLocked", "OpenAskedBy"} {
		t.Run(method, func(t *testing.T) {
			s := lifecycleStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer unlock()
			written := make(chan error, 1)
			go func() { written <- holdWriter(s, sample("owner"), entered, release) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("writer did not acquire lock")
			}
			read := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				if method == "ReadLocked" {
					st, err := s.ReadLocked()
					if err == nil && len(st.Questions) != 1 {
						err = fmt.Errorf("read %d questions", len(st.Questions))
					}
					read <- err
				} else {
					qs, err := s.OpenAskedBy("room", "owner")
					if err == nil && len(qs) != 1 {
						err = fmt.Errorf("read %d open questions", len(qs))
					}
					read <- err
				}
			}()
			<-started
			databasetest.WaitForWriter(t, 0, read)
			unlock()
			select {
			case err := <-written:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("writer did not finish")
			}
			select {
			case err := <-read:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reader did not finish")
			}
		})
	}
}

func TestAskChecksLifecycleAfterTakingStoreLock(t *testing.T) {
	s := lifecycleStore(t)
	writeQuestionSession(t, s, fmt.Sprintf(`{"id":%q}`, questionSession))
	asked := make(chan error, 1)
	// The record changes inside the store transaction that Ask waits on.
	err := s.updateTx(func(conn *sql.Conn, _ *State) error {
		started := make(chan struct{})
		go func() { close(started); _, err := s.Ask(sample(questionSession), nil); asked <- err }()
		<-started
		databasetest.WaitForWriter(t, 0, asked)
		return sessionlife.SaveTx(conn, questionSession, json.RawMessage(fmt.Sprintf(`{"id":%q,"closing":{"token":"clear","pid":%d}}`, questionSession, os.Getpid())))
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-asked:
		if err == nil {
			t.Fatal("ask used lifecycle state from before the store transaction")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not finish")
	}
}

func TestOpenAskedByKeepsRoomFilterAndNewestOrder(t *testing.T) {
	s := lifecycleStore(t)
	first, err := s.Ask(sample("owner"), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Ask(sample("owner"), nil)
	if err != nil {
		t.Fatal(err)
	}
	other := sample("owner")
	other.Room = "other"
	if _, err = s.Ask(other, nil); err != nil {
		t.Fatal(err)
	}
	qs, err := s.OpenAskedBy("room", "owner")
	if err != nil || len(qs) != 2 || qs[0].ID != second.ID || qs[1].ID != first.ID {
		t.Fatalf("open questions: %+v, %v", qs, err)
	}
}

func TestQuestionPublicationClearsDormancy(t *testing.T) {
	for _, role := range []string{"asker", "answerer"} {
		t.Run(role, func(t *testing.T) {
			s := lifecycleStore(t)
			writeQuestionSession(t, s, `{"id":"`+questionSession+`","status":"dormant","finished":"2026-10-17T10:00:00Z"}`)
			q := sample("owner")
			q.To = "owner"
			if role == "asker" {
				q.Asker = questionSession
			} else {
				q.To = questionSession
			}
			got, err := s.Ask(q, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Asker != q.Asker || got.To != q.To {
				t.Fatalf("question assignment changed: %+v", got)
			}
			data, err := recordtest.ReadRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), questionSession)
			if err != nil {
				t.Fatal(err)
			}
			var meta struct {
				Status   string
				Finished time.Time
			}
			if err = json.Unmarshal(data, &meta); err != nil {
				t.Fatal(err)
			}
			if meta.Status != "idle" || !meta.Finished.IsZero() {
				t.Fatalf("question owner metadata = %s", data)
			}
		})
	}
}
