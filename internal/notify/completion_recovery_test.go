package notify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestCompletionRetriesFailedRoomAppend(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	// The append error is injected; this deadline only bounds a broken test.
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()

	path := filepath.Join(e.svc.Rooms.Root, rec.ID, "messages.jsonl")
	saved := path + ".saved"
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	blocked := true
	restore := func() {
		if blocked {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(saved, path); err != nil {
				t.Fatal(err)
			}
			blocked = false
		}
	}
	t.Cleanup(restore)
	// A directory fails the room append even when the test runs as root.
	if result, err := e.svc.CompleteSessionTurn(ctx, child.ID, 1); err == nil {
		t.Fatalf("blocked append succeeded: %+v", result.Message)
	}
	turn, err := e.mgr.Store.ReadTurn(child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Reported {
		t.Error("failed room append acknowledged the completion")
	}
	restore()
	if posts := completionRecoveryPosts(t, e.svc, rec.ID, child); len(posts) != 0 {
		t.Fatalf("failed append stored %d completions", len(posts))
	}
	result, err := e.svc.CompleteSessionTurn(ctx, child.ID, 1)
	if err != nil || result.Message.ID == "" {
		t.Fatalf("retry lost the completion: %+v, %v", result.Message, err)
	}
	if posts := completionRecoveryPosts(t, e.svc, rec.ID, child); len(posts) != 1 || posts[0].ID != result.Message.ID {
		t.Fatalf("retry stored completions %+v; want only %s", posts, result.Message.ID)
	}
	turn, err = e.mgr.Store.ReadTurn(child.ID, 1)
	if err != nil || !turn.Reported {
		t.Fatalf("stored completion was not acknowledged: %+v, %v", turn, err)
	}
}

func TestCompletionRecoversStoredPostAfterFailedAcknowledgement(t *testing.T) {
	for _, changeParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("change-parent=%t", changeParent), func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := e.start(rec, "lead", nil)
			child := e.start(rec, "child", &parent)
			// The trigger must cause the failure, rather than a load-sensitive deadline.
			ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
			defer cancel()
			unblock := blockCompletionAcknowledgement(t, ctx, e, child.ID)
			_, err := e.svc.CompleteSessionTurn(ctx, child.ID, 1)
			if err == nil || !strings.Contains(err.Error(), "completion acknowledgement blocked") {
				t.Fatalf("acknowledgement failure = %v; want the injected failure", err)
			}
			posts := completionRecoveryPosts(t, e.svc, rec.ID, child)
			if len(posts) != 1 {
				t.Fatalf("acknowledgement failure left %d completions; want one stored post", len(posts))
			}
			stored := posts[0]
			if stored.ID == "" || stored.SessionIDs[parent.Address()] != parent.ID {
				t.Fatalf("stored completion lost its exact parent: %+v", stored)
			}
			turn, err := e.mgr.Store.ReadTurn(child.ID, 1)
			if err != nil || turn.Reported {
				t.Fatalf("failed acknowledgement was finalized: %+v, %v", turn, err)
			}

			if !changeParent {
				// Consume the post before acknowledging it, as a live engine can.
				parent = e.wait(parent.ID, 2)
			}

			var replacement subagent.Info
			if changeParent {
				if _, err := e.mgr.Store.UpdateMeta(parent.ID, func(meta *subagent.Meta) {
					meta.Name, meta.Closed = "retired-lead", time.Now()
				}); err != nil {
					t.Fatal(err)
				}
				replacement = e.start(rec, parent.Name, nil)
				if replacement.ID == parent.ID || replacement.Address() != parent.Address() {
					t.Fatalf("replacement did not reuse the old address: %+v", replacement)
				}
				if _, err := e.mgr.Store.UpdateMeta(child.ID, func(meta *subagent.Meta) {
					meta.ParentSession = replacement.ID
				}); err != nil {
					t.Fatal(err)
				}
			}

			recovered := reopenCompletionService(t, e)
			unblock()
			result, err := recovered.CompleteSessionTurn(ctx, child.ID, 1)
			if err != nil || result.Message.ID != stored.ID {
				t.Fatalf("recovery changed post %s: %+v, %v", stored.ID, result.Message, err)
			}
			posts = completionRecoveryPosts(t, recovered, rec.ID, child)
			if len(posts) != 1 || !reflect.DeepEqual(posts[0], stored) {
				t.Fatalf("recovery changed or duplicated the stored completion: %+v; want %+v", posts, stored)
			}
			turn, err = recovered.Sessions.Store.ReadTurn(child.ID, 1)
			if err != nil || !turn.Reported {
				t.Fatalf("recovery did not acknowledge the stored completion: %+v, %v", turn, err)
			}
			if !changeParent {
				started, err := recovered.Sessions.TickSessions(ctx)
				if err != nil || len(started) != 0 {
					t.Fatalf("replayed completion woke a new turn: %v, %v", started, err)
				}
				queue, err := recovered.Sessions.Store.SessionQueue(recovered.Rooms, parent.ID)
				if err != nil || len(queue) != 1 || queue[0].ID != stored.ID || queue[0].State != "delivered" || queue[0].Turn != 2 {
					t.Fatalf("completion delivery after retry = %+v, %v; want one delivery to turn 2", queue, err)
				}
			}
			if changeParent {
				queue, err := recovered.Sessions.Store.SessionQueue(recovered.Rooms, replacement.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range queue {
					if item.ID == stored.ID {
						t.Fatalf("old completion rebound to new session %s: %+v", replacement.ID, item)
					}
				}
			}
		})
	}
}

func completionRecoveryPosts(t *testing.T, svc *Service, roomID string, child subagent.Info) []room.Message {
	t.Helper()
	posts, err := svc.Rooms.Messages(roomID)
	if err != nil {
		t.Fatal(err)
	}
	var completions []room.Message
	for _, post := range posts {
		if ev, ok := post.TurnResult(); ok && ev.SessionID == child.ID && ev.Turn == 1 && ev.State == room.TurnCompleted {
			completions = append(completions, post)
		}
	}
	return completions
}

func blockCompletionAcknowledgement(t *testing.T, ctx context.Context, e *testEnv, sessionID string) func() {
	t.Helper()
	dataDir := filepath.Dir(e.mgr.Store.Root)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	// Scope the trigger to this turn's false-to-true acknowledgement. Planning
	// writes with Reported=false must still commit before the room append.
	query := fmt.Sprintf(`CREATE TRIGGER fail_completion_ack BEFORE UPDATE OF turn ON session_turns
WHEN NEW.session = '%s' AND NEW.n = 1
 AND COALESCE(json_extract(CAST(OLD.turn AS TEXT), '$.reported'), 0) = 0
 AND json_extract(CAST(NEW.turn AS TEXT), '$.reported') = 1
BEGIN SELECT RAISE(ABORT, 'completion acknowledgement blocked'); END`, sessionID)
	if _, err := db.ExecContext(ctx, query); err != nil {
		t.Fatal(err)
	}
	unblock := func() {
		// Reopen by data folder so cleanup also works after a service restart.
		db, err := database.Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DROP TRIGGER IF EXISTS fail_completion_ack"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(unblock)
	return unblock
}

func reopenCompletionService(t *testing.T, e *testEnv) *Service {
	t.Helper()
	dataDir := filepath.Dir(e.mgr.Store.Root)
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(dataDir); err != nil {
		t.Fatal(err)
	}
	mgr := &subagent.Manager{
		Store:    &subagent.Store{Root: e.mgr.Store.Root},
		Profiles: e.mgr.Profiles,
		Settings: e.mgr.Settings,
		Spawner:  e.mgr.Spawner,
	}
	return New(&room.Store{Root: e.svc.Rooms.Root}, mgr)
}
