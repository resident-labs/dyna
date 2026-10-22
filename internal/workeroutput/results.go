package workeroutput

import "strings"

func CodexSessionID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		ev, ok := parseRecord[codexRecord]([]byte(line))
		if ok && ev.Type == "thread.started" && ev.ThreadID != "" {
			return ev.ThreadID
		}
	}
	return ""
}

func AntigravitySessionID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		ev, ok := parseRecord[antigravityRecord]([]byte(line))
		if !ok {
			continue
		}
		if ev.Event == "init" && ev.ConversationID != "" {
			return ev.ConversationID
		}
		if ev.Event == "result" && ev.Result.ConversationID != "" {
			return ev.Result.ConversationID
		}
	}
	return ""
}

func AntigravityOutput(stdout string) string {
	var response string
	for _, line := range strings.Split(stdout, "\n") {
		ev, ok := parseRecord[antigravityRecord]([]byte(line))
		if ok && ev.Event == "result" && strings.TrimSpace(ev.Result.Response) != "" {
			response = strings.TrimSpace(ev.Result.Response)
		}
	}
	return response
}

func OpenCodeSessionID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		ev, ok := parseRecord[openCodeRecord]([]byte(line))
		if ok && ev.SessionID != "" {
			return ev.SessionID
		}
	}
	return ""
}

func OpenCodeOutput(stdout string) string {
	var lastMessage string
	parts := map[string][]string{}
	for _, line := range strings.Split(stdout, "\n") {
		ev, ok := parseRecord[openCodeRecord]([]byte(line))
		if !ok || ev.Type != "text" || strings.TrimSpace(ev.Part.Text) == "" {
			continue
		}
		messageID := ev.Part.MessageID
		if messageID == "" {
			messageID = "_unknown"
		}
		lastMessage = messageID
		parts[messageID] = append(parts[messageID], strings.TrimSpace(ev.Part.Text))
	}
	return strings.Join(parts[lastMessage], "\n")
}
