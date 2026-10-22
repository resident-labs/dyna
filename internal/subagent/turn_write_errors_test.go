package subagent

import (
	"errors"
	"strings"
	"testing"
)

// A turn whose record cannot be marked running fails with that error. It
// must not run while its record still says queued.
func TestTurnFailsWhenItCannotBeMarkedRunning(t *testing.T) {
	e := newTestEnv(t)
	db, err := e.m.Store.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	// The store refuses only the write that marks a turn running.
	if _, err := db.Exec(`CREATE TRIGGER refuse_running BEFORE UPDATE ON session_turns
WHEN json_extract(CAST(NEW.turn AS TEXT), '$.status') = 'running'
BEGIN SELECT RAISE(ABORT, 'refused for the test'); END`); err != nil {
		t.Fatal(err)
	}
	info, _ := e.start("RESPOND: done")
	_, turn := e.wait(info.ID, 1)
	if turn.Status != TurnError || !strings.Contains(turn.Error, "mark turn 1 running") || !strings.Contains(turn.Error, "refused for the test") {
		t.Fatalf("turn = %s %q; want an error naming the failed running write", turn.Status, turn.Error)
	}
}

// Turns lists a turn it cannot read, with the read error, instead of
// leaving a gap that show and the transcript cannot explain.
func TestTurnsListsAnUnreadableTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	e.m.Store.decodeTurn = func([]byte, any) error { return errors.New("damaged for the test") }
	t.Cleanup(func() { e.m.Store.decodeTurn = nil })
	turns, err := e.m.Store.Turns(info.ID)
	if err != nil || len(turns) != 1 || turns[0].N != 1 || turns[0].Status != TurnUnreadable || !strings.Contains(turns[0].Error, "damaged for the test") {
		t.Fatalf("Turns = %+v, %v; want turn 1 listed as unreadable with its error", turns, err)
	}
}
