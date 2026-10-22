package subagent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Pending room work decodes only the turns that can still change. Before
// dyna.db held finished turns' steering, every check decoded every turn file,
// so its cost grew with the session's history.
func TestPendingRoomMessagesDecodesOnlyUnfinishedTurns(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	e.joinRunners()()
	const turns = 8
	for n := 2; n <= turns; n++ {
		status := TurnOK
		if n == turns {
			status = TurnRunning
		}
		turn := Turn{N: n, Status: status, Created: time.Now(), Steering: []Steer{{ID: fmt.Sprintf("msg_%d", n), Delivered: n%2 == 0}}}
		if err := e.m.Store.writeTurn(info.ID, turn); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Turns = turns })
	if err != nil {
		t.Fatal(err)
	}
	decoded := map[int]int{}
	e.m.Store.decodeTurn = func(data []byte, value any) error {
		var turn Turn
		if err := json.Unmarshal(data, &turn); err != nil {
			return err
		}
		decoded[turn.N]++
		return json.Unmarshal(data, value)
	}
	for range 2 {
		if _, err := e.m.pendingRoomMessages(meta); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n < turns; n++ {
		if decoded[n] > 1 {
			t.Errorf("finished turn %d decoded %d times, want at most once", n, decoded[n])
		}
	}
	if decoded[turns] != 2 {
		t.Errorf("running turn %d decoded %d times, want every call", turns, decoded[turns])
	}
	delivered, err := e.m.Store.deliveredIDs(info.ID, turns)
	if err != nil {
		t.Fatal(err)
	}
	for n := 2; n <= turns; n++ {
		if id := fmt.Sprintf("msg_%d", n); delivered[id] != (n%2 == 0) {
			t.Errorf("%s delivered = %v, want %v", id, delivered[id], n%2 == 0)
		}
	}
}
