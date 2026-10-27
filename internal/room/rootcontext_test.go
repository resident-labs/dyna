package room

import "testing"

// TestRootContext is the one classifier both roots render from: the external
// watch holds quiet posts and delivers wake posts, and the internal root
// lists quiet categories with its next turn that has posts.
func TestRootContext(t *testing.T) {
	const roomName = "work"
	rootAddr := roomName + ":" + RootAgent
	for _, tc := range []struct {
		name     string
		msg      Message
		decision RootDecision
		category string
	}{
		{
			name:     "owner DM to an agent is quiet context",
			msg:      Message{Wake: true, ID: "dm", From: roomName, Human: true, Kind: "message", To: []string{roomName + ":worker"}, Via: []string{"@worker"}},
			decision: RootQuiet, category: RootOwnerDM,
		},
		{
			name:     "owner post naming root and an agent wakes",
			msg:      Message{Wake: true, ID: "both", From: roomName, Human: true, Kind: "message", To: []string{roomName, roomName + ":worker"}, Via: []string{"@root", "@worker"}},
			decision: RootWake,
		},
		{
			name:     "owner post to root wakes",
			msg:      Message{Wake: true, ID: "owner", From: roomName, Human: true, Kind: "message", To: []string{roomName}},
			decision: RootWake,
		},
		{
			name:     "an unstamped owner script naming root wakes",
			msg:      Message{Wake: true, ID: "script", From: roomName, Kind: "message", To: []string{roomName}, Via: []string{"@" + roomName + ":root"}},
			decision: RootWake,
		},
		{
			name:     "an unstamped owner script naming root with no wake is quiet",
			msg:      Message{ID: "bridge", From: roomName, Kind: "message", To: []string{roomName}, Via: []string{"@" + roomName + ":root"}},
			decision: RootQuiet,
		},
		{
			name:     "agent message to the owner is quiet",
			msg:      Message{ID: "note", From: roomName + ":worker", To: []string{roomName}},
			decision: RootQuiet, category: RootQuietPost,
		},
		{
			name:     "an issue to the owner is quiet context of every kind",
			msg:      Message{ID: "issue", From: roomName + ":worker", Kind: string(FamilyIssue), Notice: EventNotice{Kind: string(IssueFiled)}, To: []string{roomName}},
			decision: RootQuiet, category: RootQuietPost,
		},
		{
			name:     "a session completion to the owner is quiet",
			msg:      Message{ID: "done", From: roomName + ":worker", Kind: "completed", OwnerReport: true, To: []string{roomName}},
			decision: RootQuiet, category: RootQuietPost,
		},
		{
			name:     "a shell result owed to the owner wakes",
			msg:      Message{Wake: true, ID: "shell", From: "shell:1", Kind: "completed", OwnerReport: true, To: []string{roomName}},
			decision: RootWake,
		},
		{
			name:     "root's own merge decision is skipped",
			msg:      Message{ID: "merge", From: roomName + ":" + RootAgent, Kind: "merge-decision", To: []string{roomName}},
			decision: RootSkip,
		},
		{
			name:     "an alert to root wakes",
			msg:      Message{Wake: true, ID: "alert", From: roomName + ":worker", Kind: "service-alert", To: []string{rootAddr}},
			decision: RootWake,
		},
		{
			name:     "a marked action request wakes",
			msg:      Message{Wake: true, ID: "ask", From: roomName + ":worker", Kind: "message", NeedsRoot: true, To: []string{roomName}},
			decision: RootWake,
		},
		{
			name:     "an engine note to the root session is input",
			msg:      Message{Wake: true, ID: "seat", From: "dyna", Kind: string(FamilySession), Notice: EventNotice{Kind: string(SessionSeat)}, To: []string{roomName, rootAddr}},
			decision: RootWake,
		},
		{
			name:     "a passive engine instruction stays quiet turn input",
			msg:      Message{ID: "passive", From: "dyna", Kind: string(FamilySession), Notice: EventNotice{Kind: string(SessionInstructions)}, To: []string{rootAddr}},
			decision: RootQuiet,
		},
		{
			name:     "an engine instruction to the owner is quiet",
			msg:      Message{ID: "instr", From: "dyna", Kind: string(FamilySession), Notice: EventNotice{Kind: string(SessionInstructions)}, To: []string{roomName}},
			decision: RootQuiet, category: RootQuietPost,
		},
		{
			name:     "the root's own reply is never context",
			msg:      Message{ID: "own", From: rootAddr, Kind: "message", To: []string{roomName}},
			decision: RootSkip,
		},
		{
			name:     "the orchestrator's own owner post is not echoed",
			msg:      Message{ID: "echo", From: roomName, To: []string{roomName}, Text: "posted as the owner"},
			decision: RootSkip,
		},
		{
			name:     "the owner's own shell post that needs root wakes",
			msg:      Message{Wake: true, ID: "cli", From: roomName, Kind: "message", NeedsRoot: true, To: []string{roomName}, Text: "Choose the merge order"},
			decision: RootWake,
		},
		{
			name:     "the owner's own shell post to the root session is passive input",
			msg:      Message{ID: "answer", From: roomName, Kind: "message", To: []string{rootAddr}, Text: "Yes, download it"},
			decision: RootQuiet,
		},
		{
			name:     "voice call lines stay out",
			msg:      Message{ID: "spoken", From: roomName + ":" + VoiceAgent, Kind: "voice", Source: SourceVoice, To: []string{roomName}},
			decision: RootSkip,
		},
		{
			name:     "a voice task post to root wakes",
			msg:      Message{Wake: true, ID: "relay", From: roomName + ":" + VoiceAgent, Kind: "message", Source: SourceVoice, To: []string{roomName}},
			decision: RootWake,
		},
		{
			name:     "a post between agents is not root context",
			msg:      Message{Wake: true, ID: "peers", From: roomName + ":a", Kind: "service-alert", To: []string{roomName + ":b"}},
			decision: RootSkip,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, category := RootContext(tc.msg, roomName)
			if decision != tc.decision || category != tc.category {
				t.Fatalf("RootContext(%s) = %d, %q; want %d, %q", tc.msg.ID, decision, category, tc.decision, tc.category)
			}
		})
	}
}

// A room named after the engine holds engine posts under the room's own
// address. A session note addressed to a worker is that worker's, never the
// owner's DM, so the root's [OWNER DMS] block keeps the owner's words.
// The sender's room is a stored identity, not text. A room rename or an
// address that looks local must not change whether root wakes for a session
// from another room.
func TestRootWakeUsesTheStoredSenderRoom(t *testing.T) {
	const roomName = "renamed"
	// A session of this room wrote the post under the room's old name. Its
	// quiet notice must not wake root.
	sameRoom := Message{
		Wake: true, ID: "msg_same", RoomID: "room_1", From: "old-name:lead",
		SenderRoomID: "room_1", SessionIDs: map[string]string{"old-name:lead": "ses_lead"},
		Kind: "notice", To: []string{roomName},
	}
	if decision, _ := RootContext(sameRoom, roomName); decision == RootWake {
		t.Fatalf("a same-room notice woke root after a rename: %+v", sameRoom)
	}
	// A session of another room asks this seat to act even when its display
	// address carries this room's name.
	otherRoom := Message{
		Wake: true, ID: "msg_other", RoomID: "room_1", From: "renamed:lead",
		SenderRoomID: "room_2", SessionIDs: map[string]string{"renamed:lead": "ses_lead"},
		Kind: "notice", To: []string{roomName},
	}
	if decision, _ := RootContext(otherRoom, roomName); decision != RootWake {
		t.Fatalf("a notice from another room did not wake root: %d", decision)
	}
}

func TestMessageSenderParsesTheAuthorKind(t *testing.T) {
	for _, tc := range []struct {
		msg  Message
		want SenderKind
	}{
		{Message{From: EngineSender}, SenderEngine},
		{Message{From: "r"}, SenderOwner},
		{Message{From: "r", Human: true}, SenderOwner},
		{Message{From: "r:" + RootAgent}, SenderRoot},
		{Message{From: "r:lead"}, SenderSession},
		{Message{From: "shell:sh_1"}, SenderShell},
		{Message{From: "run:wf_1"}, SenderRun},
	} {
		if got := tc.msg.Sender(); got != tc.want {
			t.Errorf("Sender(%q) = %q, want %q", tc.msg.From, got, tc.want)
		}
	}
}

func TestRootContextEngineNoteIsNoOwnerDM(t *testing.T) {
	const roomName = EngineSender
	for _, note := range []struct {
		family EventFamily
		typ    EventType
	}{
		{FamilySession, EventType(SessionInstructions)},
		{FamilySession, EventType(SessionRenamed)},
		{FamilyWorktree, TypeKept},
	} {
		msg := Message{ID: "note", From: EngineSender, Kind: string(note.family), Notice: EventNotice{Kind: string(note.typ)}, To: []string{roomName + ":worker"}}
		if decision, category := RootContext(msg, roomName); decision != RootSkip || category != "" {
			t.Fatalf("an engine %s/%s to a worker = %d, %q; want no root context", note.family, note.typ, decision, category)
		}
	}
	// The owner's own DM keeps its quiet owner-DM category.
	owner := Message{ID: "dm", From: roomName, Human: true, Kind: "message", To: []string{roomName + ":worker"}}
	if decision, category := RootContext(owner, roomName); decision != RootQuiet || category != RootOwnerDM {
		t.Fatalf("the owner's DM = %d, %q; want quiet %q", decision, category, RootOwnerDM)
	}
}
