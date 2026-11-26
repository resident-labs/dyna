package sessiondb

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
)

const (
	maxMessageFiles     = 256 // ChildWork:45
	maxDetachedChildren = 64  // ChildWork:46
	pendingReplyTail    = 64 << 10
	transcriptByteCap   = 256 << 10
	highPriority        = "MESSAGE_PRIORITY_HIGH"
	lowPriority         = "MESSAGE_PRIORITY_LOW"
)

func brain(sessionID string, rest ...string) []string {
	return append([]string{"antigravity-acp", "brain", sessionID, ".system_generated"}, rest...)
}

// readJSON reads a record file the managed-path proof accepts, within the size bound, into v.
func readJSON(root string, segments []string, v any) bool {
	path, info, ok := managedPath(root, segments, true)
	if !ok || info.Size() > maxFileBytes {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

// PendingReply is true when agy has a high-priority wake queued in the session's inbox, or already dequeued as the trailing
// SYSTEM_MESSAGE step of transcript.jsonl with no PLANNER_RESPONSE yet. It fails closed when records are missing, linked or
// unreadable (ChildWork:1460-1532).
func PendingReply(profileDir, sessionID string) bool {
	root, ok := profileRoot(profileDir)
	if !ok || !nativeID.MatchString(sessionID) {
		return false
	}
	if unreadWakingMessage(root, sessionID) {
		return true
	}
	last := lastTranscriptLine(root, sessionID)
	if last == nil || last.Type != "SYSTEM_MESSAGE" {
		return false
	}
	return last.Content == nil || strings.Contains(*last.Content, "priority="+highPriority) || !strings.Contains(*last.Content, "priority="+lowPriority)
}

func unreadWakingMessage(root, sessionID string) bool {
	dir := brain(sessionID, "messages")
	path, _, ok := managedPath(root, dir, false)
	if !ok {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	var names []string
	receipts := false
	for _, e := range entries {
		switch {
		case e.Name() == "read.json":
			receipts = true
		case strings.HasSuffix(e.Name(), ".json"):
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 || len(names) > maxMessageFiles {
		return false
	}
	read := map[string]bool{}
	if receipts && !readJSON(root, append(slices.Clone(dir), "read.json"), &read) {
		return false
	}
	for _, name := range names {
		fileID := strings.TrimSuffix(name, ".json")
		if read[fileID] {
			continue
		}
		var m struct {
			ID       string `json:"id"`
			Priority string `json:"priority"`
		}
		if !readJSON(root, append(slices.Clone(dir), name), &m) || (m.ID != "" && read[m.ID]) {
			continue
		}
		if m.Priority == highPriority {
			return true
		}
	}
	return false
}

type transcriptLine struct {
	Type    string  `json:"type"`
	Content *string `json:"content"`
}

func lastTranscriptLine(root, sessionID string) *transcriptLine {
	path, _, ok := managedPath(root, brain(sessionID, "logs", "transcript.jsonl"), true)
	if !ok {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil
	}
	for _, cap := range []int64{pendingReplyTail, transcriptByteCap} {
		start := max(0, info.Size()-cap)
		buf := make([]byte, info.Size()-start)
		n, err := f.ReadAt(buf, start)
		if err != nil && err != io.EOF {
			return nil
		}
		buf = bytes.TrimRight(buf[:n], "\r\n")
		if len(buf) == 0 {
			if start == 0 {
				return nil
			}
			continue
		}
		cut := bytes.LastIndexByte(buf, '\n')
		if cut == -1 && start > 0 {
			continue
		}
		var line transcriptLine
		if json.Unmarshal(buf[cut+1:], &line) != nil || line.Type == "" {
			return nil
		}
		return &line
	}
	return nil
}

type childMessage struct {
	Sender         string `json:"sender"`
	SourceMetadata struct {
		Tool struct {
			ConversationID string `json:"conversationId"`
			ToolCall       struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"toolCall"`
		} `json:"tool"`
	} `json:"sourceMetadata"`
}

// readInbox is a conversation's inbox. ok is false past a bound or on a file it cannot read, so nothing is proven.
func readInbox(root, conversationID string) ([]childMessage, bool) {
	dir := brain(conversationID, "messages")
	path, _, found := managedPath(root, dir, false)
	if !found {
		return nil, true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, false
	}
	var out []childMessage
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || e.Name() == "read.json" {
			continue
		}
		if len(out) >= maxMessageFiles {
			return nil, false
		}
		var m childMessage
		if !readJSON(root, append(slices.Clone(dir), e.Name()), &m) {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

// ChildrenAtWork returns the launching tool call ids, among toolCallIDs, of agy 1.5 native children that outlive their
// invoke_subagent call: a child is done once its message is in the parent's inbox or its record says it was killed. None
// when the records prove nothing (ChildWork:1534-1586).
func ChildrenAtWork(profileDir, rootSessionID string, toolCallIDs []string) []string {
	root, ok := profileRoot(profileDir)
	if !ok || !nativeID.MatchString(rootSessionID) {
		return nil
	}
	dir, _, ok := managedPath(root, brain(rootSessionID, "subagents"), false)
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var childIDs []string
	for _, e := range entries {
		if id, isJSON := strings.CutSuffix(e.Name(), ".json"); isJSON && nativeID.MatchString(id) {
			childIDs = append(childIDs, id)
		}
	}
	if len(childIDs) > maxDetachedChildren {
		return nil
	}
	parent, ok := readInbox(root, rootSessionID)
	if !ok {
		return nil
	}
	reported := map[string]bool{}
	for _, m := range parent {
		reported[m.Sender] = true
	}
	var roster struct {
		Members map[string]struct {
			ConversationID string `json:"conversation_id"`
			Role           string `json:"role"`
			DesiredState   string `json:"desired_state"`
			Phase          string `json:"phase"`
			WakeMode       string `json:"wake_mode"`
		} `json:"members"`
	}
	finished := map[string]bool{} // killed or parked swarm members
	if readJSON(root, brain(rootSessionID, "swarm", "members.json"), &roster) {
		for _, m := range roster.Members {
			if nativeID.MatchString(m.ConversationID) && m.Role != "coordinator" &&
				(m.DesiredState == "KILLED" || m.Phase == "KILLED" || m.WakeMode == "mentions" || m.WakeMode == "anything") {
				finished[m.ConversationID] = true
			}
		}
	}
	var atWork []string
	add := func(id string) {
		if slices.Contains(toolCallIDs, id) && !slices.Contains(atWork, id) {
			atWork = append(atWork, id)
		}
	}
	for _, id := range childIDs {
		if reported[id] || finished[id] {
			continue
		}
		var record struct {
			ConversationID string `json:"conversationId"`
			State          string `json:"state"`
		}
		// manage_subagents kill only marks the record; a killed child never reports.
		if readJSON(root, brain(rootSessionID, "subagents", id+".json"), &record) &&
			(record.ConversationID == "" || record.ConversationID == id) && record.State == "SUBAGENT_STATE_KILLED" {
			continue
		}
		add("swarm:" + id)
		inbox, ok := readInbox(root, id)
		if !ok {
			return nil
		}
		for _, m := range inbox {
			call := m.SourceMetadata.Tool
			if m.Sender == rootSessionID && (call.ConversationID == "" || call.ConversationID == rootSessionID) &&
				(call.ToolCall.Name == "invoke_subagent" || call.ToolCall.Name == "swarm") {
				add(call.ToolCall.ID)
			}
		}
	}
	return atWork
}
