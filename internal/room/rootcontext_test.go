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
