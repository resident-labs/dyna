package subagent

import (
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

type closeLockSpawner struct{}

func (closeLockSpawner) Spawn(_ *Manager, _ string, _ int, lock *os.File) error {
	return lock.Close()
}

func TestTurnRecordsWhatWokeItAndHowEachPostWasDelivered(t *testing.T) {
	e := newTestEnv(t)
	e.m.Spawner = closeLockSpawner{}
	info := completedFixtureSession(t, e)

	if got := room.DeliveryReceipt(room.DeliveryQuiet); got == "session-queued" || got != "session-quiet" {
		t.Fatalf("quiet receipt = %q, want session-quiet", got)
	}
	quietMsg, err := e.m.RoomStore().Post(info.RoomID, room.PostInput{
		Wake: false, Human: true, From: info.Room, To: []string{info.Address()}, Via: []string{"@" + info.Address()},
		Text: "quiet context from the bridge",
	})
	if err != nil {
		t.Fatal(err)
	}
	wakingMsg, err := e.m.RoomStore().Post(info.RoomID, room.PostInput{
		Wake: true, Human: true, From: info.Room, To: []string{info.Address()}, Via: []string{"@" + info.Address()},
		Text: "please act",
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.m.Wake(e.ctx, info.ID, WakeOptions{MessageID: wakingMsg.ID})
	if err != nil || res.Steered || res.Turn != 2 {
		t.Fatalf("wake = %+v, %v", res, err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Wake.Kind != WakePost || turn.Wake.PostID != wakingMsg.ID || turn.Wake.Sender != "the owner" {
		t.Fatalf("wake cause = %+v", turn.Wake)
	}
	if line := turn.Wake.Line(); line != "woke on "+wakingMsg.ID+" from the owner" {
		t.Fatalf("wake line = %q", line)
	}
	if turn.Wake.HeaderLine() != "This turn "+turn.Wake.Line()+"." {
		t.Fatalf("header = %q", turn.Wake.HeaderLine())
	}

	keys := room.DeliveryKeys(info.ID, info.Address())
	rows, err := e.m.rooms().SessionDeliveries(info.RoomID, keys, []string{wakingMsg.ID, quietMsg.ID})
	if err != nil {
		t.Fatal(err)
	}
	woke := rows[wakingMsg.ID]
	if woke.How != room.DeliveryWoke || woke.ClaimedTurn != 2 || woke.DeliveredAt <= 0 {
		t.Fatalf("waking delivery = %+v", woke)
	}
	if rows[quietMsg.ID].How == room.DeliveryWoke {
		t.Fatalf("quiet post was recorded as the wake: %+v", rows[quietMsg.ID])
	}

	if err := e.m.noteIncluded(e.ctx, info.ID, 2, ClaimedInput{Posts: []ClaimedPost{
		{Steer: Steer{ID: quietMsg.ID}},
		{Steer: Steer{ID: wakingMsg.ID}},
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err = e.m.rooms().SessionDeliveries(info.RoomID, keys, []string{wakingMsg.ID, quietMsg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rows[wakingMsg.ID].How != room.DeliveryWoke {
		t.Fatalf("batched note downgraded the cause: %+v", rows[wakingMsg.ID])
	}
	held := rows[quietMsg.ID]
	if held.How != room.DeliveryQuietContext || held.DeliveredAt <= 0 || !strings.Contains(held.Summary(info.Address()), "quiet context") {
		t.Fatalf("quiet delivery = %+v summary %q", held, held.Summary(info.Address()))
	}

	facts := e.m.inputFacts(info.Meta, []room.Message{wakingMsg, quietMsg})
	if facts[wakingMsg.ID].Input != "waking" || facts[quietMsg.ID].Input != "quiet" {
		t.Fatalf("input facts = %+v", facts)
	}
	header := wakingMsg.Header(facts[wakingMsg.ID])
	sent := time.UnixMilli(wakingMsg.TS).Local().Format("2006-01-02 15:04:05")
	if !strings.Contains(header, "Input: waking") || !strings.Contains(header, "Sent: "+sent) || !strings.Contains(header, "Delivered:") {
		t.Fatalf("post header missing delivery facts:\n%s", header)
	}
}
