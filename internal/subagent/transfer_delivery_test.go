package subagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/workeroutput"
)

// The packet must lead with live work: goals with the keeper's latest journal
// line, open items and the PRs the session authors or reviews, with their heads.
func TestHandoffPacketLeadsWithActiveWork(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: the original spawn task")
	info, _ = e.wait(info.ID, 1)
	g, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "Finish the decoder", Holders: []string{info.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.goalStore().AddItem(info.RoomID, g.ID, "owner", "check the fuzz corpus", ""); err != nil {
		t.Fatal(err)
	}
	line := goals.JournalLine{Agent: info.Address(), Status: "waiting", Summary: "parser is done, tests remain; #1046 is open", Goals: []string{g.ID}}
	if _, err := e.m.goalStore().AddJournal(info.RoomID, goals.JournalEntry{Entries: []goals.JournalLine{line}}); err != nil {
		t.Fatal(err)
	}
	writePRLink(t, info, "author", "open", true)
	root := prstate.Root(filepath.Dir(e.m.Store.Root))
	st, err := prstate.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Records {
		r.Head = "abc123"
	}
	if err := prstate.Write(root, st); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	packet, _, err := e.m.handoffPacket(meta, TransferState{Dir: info.Dir, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Goals:", "Goal " + g.ID + ": Finish the decoder", "Finish the decoder",
		"Latest progress: waiting: parser is done, tests remain; #1046 is open",
		"Item i1: check the fuzz corpus",
		"PR #17 open https://github.com/owner/repo/pull/17 roles: author head: abc123",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("handoff packet lacks %q:\n%s", want, packet)
		}
	}
	if strings.Contains(packet, "the original spawn task") {
		t.Fatalf("packet copied the spawn task instead of pointing at it:\n%s", packet)
	}
}

// The packet names commands for the old conversation. It inlines no room post
// and no transcript text, whatever their delivery state.
func TestHandoffPacketInlinesNoTranscriptOrRoomText(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: legitimate original task")
	info, _ = e.wait(info.ID, 1)
	post := func(from, kind, text string) room.Message {
		t.Helper()
		msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: from, Kind: kind, To: []string{info.Address()}, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	valid := post(info.Room+":reporter", "message", "legitimate pending input")
	cancelled := post(info.Room, "message", "cancelled input")
	dismissed := post(info.Room+":reporter", "message", "dismissed input")
	notice, err := e.m.rooms().PostEvent(info.RoomID, room.SessionEvent{Type: room.SessionInstructions, Session: info.Address(), Detail: "notice input"}, info.Address())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.DeletePendingSteer(e.m.rooms(), info.ID, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	ownerDismissEnv(t)
	if err := e.m.DismissPendingPost(e.ctx, info.ID, dismissed.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: info.Address(), To: []string{info.Room}, Text: "legitimate prior room output"}); err != nil {
		t.Fatal(err)
	}
	norm, _ := e.m.Store.NormalizedPath(info.ID)
	f, _ := os.OpenFile(norm, os.O_APPEND|os.O_WRONLY, 0600)
	event, _ := json.Marshal(workeroutput.Event{Kind: "message", Role: "assistant", Text: "I edited parser.go; next run go test ./decoder."})
	f.Write(append(event, '\n'))
	f.Close()
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	packet, _, err := e.m.handoffPacket(meta, TransferState{Dir: info.Dir, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{valid.Text, cancelled.Text, dismissed.Text, notice.Text, "legitimate original task", "legitimate prior room output", "I edited parser.go", "go test ./decoder"} {
		if strings.Contains(packet, text) {
			t.Fatalf("packet inlined %q; it must point at the old context instead:\n%s", text, packet)
		}
	}
	for _, want := range []string{
		"dyna subagent output " + info.ID + " --turn 1 --transcript",
		"(any turn's transcript)",
		"dyna subagent history " + info.ID + " --search '<pattern>'",
		"dyna room log " + info.RoomID,
		"dyna subagent transfer status " + info.ID,
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("packet lacks the read command %q:\n%s", want, packet)
		}
	}
	if strings.Contains(packet, "--room") {
		t.Fatal("read commands must use the receiving session's room")
	}
}

// A packet with no written handoff keeps its absence note inside the bound,
// even next to the limit where the note alone could push it over.
func TestHandoffPacketNoHandoffStaysBounded(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: task")
	info, _ = e.wait(info.ID, 1)
	var ids []string
	for range 4 {
		g, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: strings.Repeat("g", 600), Holders: []string{info.ID}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, g.ID)
		for j := range 4 {
			if _, err := e.m.goalStore().AddItem(info.RoomID, g.ID, "owner", fmt.Sprintf("%d:%s", j, strings.Repeat("i", 200)), ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e.m.goalStore().AddJournal(info.RoomID, goals.JournalEntry{Entries: []goals.JournalLine{
		{Agent: info.Address(), Status: "waiting", Summary: strings.Repeat("p", 400), Goals: ids},
	}}); err != nil {
		t.Fatal(err)
	}
	writePRLink(t, info, "author", "open", true)
	root := prstate.Root(filepath.Dir(e.m.Store.Root))
	setHead := func(head string) {
		t.Helper()
		st, err := prstate.Read(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range st.Records {
			r.Head = head
		}
		if err := prstate.Write(root, st); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The PR head is unclipped, so it tunes the packet by the byte to land on
	// each side of the bound. There is no written handoff in either case.
	build := func(headSize int) string {
		t.Helper()
		setHead(strings.Repeat("h", headSize))
		packet, _, err := e.m.handoffPacket(meta, TransferState{Dir: info.Dir, Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return packet
	}
	setHead("h")
	base, _, err := e.m.handoffPacket(meta, TransferState{Dir: info.Dir, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		size int
		near bool
	}{{"just under", handoffPacketLimit - 60, true}, {"just over", handoffPacketLimit + 50, false}} {
		t.Run(tc.name, func(t *testing.T) {
			headSize := 1 + tc.size - len(base)
			if headSize < 1 {
				t.Fatalf("fixture packet is already %d bytes; it cannot be tuned to %d", len(base), tc.size)
			}
			packet := build(headSize)
			if len(packet) > handoffPacketLimit {
				t.Fatalf("no-handoff packet is %d bytes, above the %d-byte bound", len(packet), handoffPacketLimit)
			}
			if strings.Contains(packet, "Written handoff:") {
				t.Fatal("packet invented a written handoff")
			}
			if !strings.Contains(packet, "dyna subagent output "+info.ID+" --turn 1 --transcript") {
				t.Fatal("bound dropped the commands that read the old conversation")
			}
			if tc.near && len(packet) < handoffPacketLimit-200 {
				t.Fatalf("packet is %d bytes, too far below the bound to test it", len(packet))
			}
			if tc.near && !strings.Contains(packet, "no written handoff") {
				t.Fatal("near-bound packet lost the note that there is no handoff")
			}
		})
	}
}

// handoffTail marks the far end of a written handoff, so a bounded packet can
// be checked for nesting the complete text.
const handoffTail = " END-OF-WRITTEN-HANDOFF"

// The packet stays small even when the session has a long written handoff and
// a long goal, and it points at the complete handoff instead of nesting it.
func TestHandoffPacketStaysBounded(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: task")
	info, _ = e.wait(info.ID, 1)
	// The goal store caps one goal's text, so this is the longest it accepts.
	if _, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: strings.Repeat("long goal text ", 120), Holders: []string{info.ID}}); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	handoff := "Open parser work. " + strings.Repeat("w", 32*1024-len("Open parser work. ")-len(handoffTail))
	handoff += handoffTail
	packet, _, err := e.m.handoffPacket(meta, TransferState{Dir: info.Dir, Reason: "test", Source: "cooperative", WrittenHandoff: handoff, Handoff: handoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) > handoffPacketLimit {
		t.Fatalf("packet is %d bytes, above the %d-byte bound", len(packet), handoffPacketLimit)
	}
	if !strings.Contains(packet, "Open parser work.") || !strings.Contains(packet, "truncated") {
		t.Fatalf("bounded handoff is not marked as truncated:\n%s", packet)
	}
	if strings.Contains(packet, handoffTail) {
		t.Fatal("packet nested the complete handoff")
	}
}
