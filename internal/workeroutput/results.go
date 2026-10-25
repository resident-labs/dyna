package workeroutput

import "strings"

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
