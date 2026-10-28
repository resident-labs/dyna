// Package turntext renders the full result of a session turn for room posts.
package turntext

import (
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/subagent"
)

const MaxFeedResultKB = 64

// ServiceArtifactText hides structured service artifacts from room feeds.
func ServiceArtifactText(meta subagent.Meta, result any) (string, bool) {
	if !meta.ServiceContract().Active() || result == nil {
		return "", false
	}
	var data []byte
	if text, ok := result.(string); ok {
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
			text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
		}
		data = []byte(strings.TrimSpace(text))
	} else {
		data, _ = json.Marshal(result)
	}
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') || !json.Valid(data) {
		return "", false
	}
	if meta.Role == "reviewer" {
		return "The review artifact is available on the assigned change.", true
	}
	return "The service artifact is available in its parent thread.", true
}

// FullCompletionText renders the result with the feed's size limit.
func FullCompletionText(address string, turn int, kind string, result any, errText string) (string, bool) {
	head := fmt.Sprintf("%s turn %d %s.", address, turn, kind)
	var body string
	switch v := result.(type) {
	case nil:
	case string:
		body = strings.TrimSpace(v)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err == nil {
			body = "```json\n" + string(b) + "\n```"
		}
	}
	text := head
	if errText != "" {
		text += " Error: " + strings.TrimSpace(errText)
	}
	if body != "" {
		text += " Result:\n\n" + body
	}
	max := MaxFeedResultKB * 1024
	if len(text) <= max {
		return text, false
	}
	cut := strings.ToValidUTF8(text[:max], "")
	if strings.Count(cut, "```")%2 == 1 {
		cut += "\n```"
	}
	return cut + fmt.Sprintf("\n\n*Cut at %d KB. Open the transcript for more.*", MaxFeedResultKB), true
}

// Render reads the exact session turn identified by the event.
func Render(store *subagent.Store, id, address string, number int, state string) (string, bool, bool) {
	turn, err := store.ReadTurn(id, number)
	if err != nil || !turn.Done() {
		return "", false, false
	}
	result := turn.Result
	if meta, err := store.ReadMeta(id); err == nil {
		if text, ok := ServiceArtifactText(meta, result); ok {
			result = text
		}
	}
	text, cut := FullCompletionText(address, number, state, result, turn.Error)
	return text, cut, true
}
