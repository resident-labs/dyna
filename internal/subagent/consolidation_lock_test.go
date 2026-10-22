package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/store"
)

func TestMutationContextCancellationDoesNotWriteAfterLockWait(t *testing.T) {
	for _, action := range []string{"promote-names", "promote-root", "promote-descendant", "transfer", "stop", "close"} {
		t.Run(action, func(t *testing.T) {
			e, _ := consolidationEnv(t)
			external := completedFixtureSession(t, e)
			root := completedFixtureSession(t, e)
			child := completedFixtureSession(t, e)
			if action == "stop" {
				// Keep a live turn without starting a runner or its retirement work.
				turnLock, err := e.m.Store.tryTurnLock(root.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer turnLock.Close()
				if _, err := e.m.Store.UpdateTurn(root.ID, 1, func(turn *Turn) {
					turn.Status, turn.Ended = TurnRunning, time.Time{}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.m.Store.UpdateMeta(root.ID, func(meta *Meta) { meta.ParentSession = external.ID; meta.Depth = 2 }); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.Store.UpdateMeta(child.ID, func(meta *Meta) { meta.ParentSession = root.ID; meta.Depth = 3 }); err != nil {
				t.Fatal(err)
			}
			var release func()
			var err error
			var heldPath string
			if action == "promote-names" {
				release, err = lockNames(e.m.Store)
				heldPath = filepath.Join(e.m.Store.root(), ".names.lock")
			} else {
				id := root.ID
				if action == "promote-descendant" {
					id = child.ID
				}
				release, err = e.m.Store.lockSessionContext(context.Background(), id)
				if err == nil {
					heldPath, err = e.m.Store.path(id, "session.lock")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			beforeRoot, err := e.m.Store.ReadMeta(root.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeChild, err := e.m.Store.ReadMeta(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			deadline, stopDeadline := context.WithTimeout(e.ctx, 750*time.Millisecond)
			defer stopDeadline()
			cancellable, cancel := context.WithCancel(deadline)
			defer cancel()
			waiting := make(chan struct{}, 1)
			ctx := store.WithFlockContentionHook(cancellable, func(path string) {
				if path == heldPath {
					select {
					case waiting <- struct{}{}:
					default:
					}
				}
			})
			done := make(chan error, 1)
			go func() {
				var err error
				switch action {
				case "promote-names", "promote-root", "promote-descendant":
					_, err = e.m.Promote(ctx, root.ID)
				case "transfer":
					_, err = e.m.TransferContext(ctx, root.ID, TransferOptions{Profile: "next"})
				case "stop":
					_, err = e.m.Stop(ctx, root.ID)
				case "close":
					_, err = e.m.CloseWithOptions(ctx, root.ID, CloseOptions{Force: true})
				}
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("operation escaped a held lock: %v", err)
			case <-waiting:
			case <-deadline.Done():
				t.Fatal("operation did not reach the held lock")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel result: %v", err)
				}
			case <-deadline.Done():
				t.Fatal("cancelled mutation remained blocked")
			}
			afterRoot, err := e.m.Store.ReadMeta(root.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterChild, err := e.m.Store.ReadMeta(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeRoot, afterRoot) || !reflect.DeepEqual(beforeChild, afterChild) {
				t.Fatal("cancelled lock wait changed hierarchy or session state")
			}
			if action == "stop" {
				turn, _ := e.m.Store.ReadTurn(root.ID, 1)
				if turn.StopAsked {
					t.Fatal("cancelled waiter requested stop")
				}
				dir, _ := e.m.Store.Dir(root.ID)
				path := filepath.Join(dir, "stop-1")
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("cancelled waiter wrote stop marker")
				}
			}
		})
	}
}

func TestPromoteRejectsReservedDescendantBeforeAnyHierarchyWrite(t *testing.T) {
	e, _ := consolidationEnv(t)
	external, _ := e.start("SLEEP:1 RESPOND: external")
	external, _ = e.wait(external.ID, 1)
	root, _ := e.start("SLEEP:1 RESPOND: ancestor")
	root, _ = e.wait(root.ID, 1)
	child, _ := e.start("SLEEP:1 RESPOND: reserved child")
	child, _ = e.wait(child.ID, 1)
	other, _ := e.start("SLEEP:1 RESPOND: other selected")
	other, _ = e.wait(other.ID, 1)
	_, _ = e.m.Store.UpdateMeta(root.ID, func(meta *Meta) { meta.ParentSession = external.ID; meta.Depth = 2 })
	_, _ = e.m.Store.UpdateMeta(child.ID, func(meta *Meta) { meta.ParentSession = root.ID; meta.Depth = 3 })
	c := consolidatePair(t, e, child, other)
	if _, err := e.m.Promote(e.ctx, root.ID); err == nil {
		t.Fatal("ancestor promotion changed a reserved descendant")
	}
	ancestor, _ := e.m.Store.ReadMeta(root.ID)
	descendant, _ := e.m.Store.ReadMeta(child.ID)
	if ancestor.ParentSession != external.ID || ancestor.Depth != 2 || descendant.ParentSession != root.ID || descendant.Depth != 3 || descendant.Consolidating != c.ID {
		t.Fatal("rejected promotion partially changed the subtree")
	}
}
