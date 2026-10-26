package subagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestEmptyFinalAfterPostedReport(t *testing.T) {
	for _, tc := range []struct {
		name, recipient            string
		workerFails, protocolFails bool
		schema, silent, liveOwes   bool
		wantOK                     bool
	}{
		{name: "parent steering clean stop", silent: true, liveOwes: true, wantOK: true},
		{name: "silent protocol error", silent: true, protocolFails: true},
		{name: "silent process error", silent: true, workerFails: true},
		{name: "silent schema requires output", silent: true, schema: true},
		{name: "parent", recipient: "parent", wantOK: true},
		{name: "root", recipient: "root", wantOK: true},
		{name: "internal root", recipient: "internal root", wantOK: true},
		{name: "unrelated post", recipient: "other", wantOK: true},
		{name: "no post", wantOK: true},
		{name: "real worker error", recipient: "root", workerFails: true},
		{name: "protocol error", recipient: "root", protocolFails: true},
		{name: "schema still requires output", recipient: "root", schema: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, mockProfile("mock"), profile.Profile{
				Name: "claude-test", Harness: profile.HarnessClaudeCode,
				Taste: 5, Intelligence: 5, Cost: 5,
				Output: &workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON},
			})
			parent := completedFixtureSession(t, e)
			root := completedFixtureSession(t, e)
			if _, err := e.m.Store.UpdateMeta(root.ID, func(m *Meta) { m.Role = RoleRoot }); err != nil {
				t.Fatal(err)
			}
			held := &heldTurnSpawner{}
			e.m.Spawner = held
			delay := time.Duration(0)
			e.m.RecoveryDelay = &delay
			info, _ := e.start("Report the result.", func(o *StartOptions) {
				o.Profile = "claude-test"
				if tc.schema {
					o.Schema = `{"type":"object"}`
				}
			})
			if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) {
				m.ParentSession, m.Depth = parent.ID, 2
			}); err != nil {
				t.Fatal(err)
			}
			if tc.silent {
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.ReportTo = nil }); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			e.m.ProcessRunner = func(_ context.Context, _ []string, stdout, _ io.Writer) error {
				calls++
				if tc.liveOwes {
					// A parent post received during execution changes the saved obligation.
					if err := e.m.noteTurn(context.Background(), info.ID, TurnNote{Turn: 1, Dispatched: &DrainedPost{Owed: parent.Address()}}); err != nil {
						return err
					}
				}
				recipient := map[string]string{"parent": parent.Address(), "root": e.room.Name, "internal root": root.Address(), "other": e.room.Name + ":sibling"}[tc.recipient]
				if recipient != "" {
					// notify records this receipt only after the room post is saved.
					if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
						turn.PostedTo = []string{recipient}
					}); err != nil {
						return err
					}
				}
				if tc.protocolFails {
					fmt.Fprintln(stdout, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"provider request failed"}`)
				} else {
					fmt.Fprintln(stdout, `{"type":"result","subtype":"success","result":""}`)
				}
				if tc.workerFails {
					return errors.New("worker process failed")
				}
				return nil
			}
			err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock)
			turn, readErr := e.m.Store.ReadTurn(info.ID, 1)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantOK {
				if err != nil || turn.Status != TurnOK || turn.Error != "" || calls != 1 {
					t.Fatalf("posted report caused a failure or recovery: turn=%+v calls=%d err=%v", turn, calls, err)
				}
			} else if err == nil || turn.Status != TurnError || calls != 2 {
				t.Fatalf("missing output or real failure was accepted: turn=%+v err=%v", turn, err)
			}
		})
	}
}

func TestPostedReportBelongsToCurrentTurn(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.PostedTo = []string{e.room.Name}
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(info.ID, Turn{N: 2, Status: TurnRunning, ReportTo: []string{e.room.Name}}); err != nil {
		t.Fatal(err)
	}
	if e.m.turnAcceptsEmptyOutput(info.ID, 2) {
		t.Fatal("a prior turn's report accepted this turn's missing result")
	}
}
