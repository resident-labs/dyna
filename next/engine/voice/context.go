package voice

import (
	"context"
	_ "embed"
	"strings"
)

//go:embed realtime_start.md
var StartInstructions string

//go:embed realtime_end.md
var EndInstructions string

// Options supplies the conversation snapshot and a final delivery independent of
// the page's canceled context. End receives an empty tail when nothing is owed.
type Options struct {
	Startup string
	End     func(context.Context, string) error
}

type transcriptLine struct{ id, role, text string }
type callTranscript struct{ lines []transcriptLine }

func (t *callTranscript) update(id, role, text string, done bool) {
	for i := range t.lines {
		if t.lines[i].id == id && t.lines[i].role == role {
			if done {
				t.lines[i].text = text
			} else {
				t.lines[i].text += text
			}
			return
		}
	}
	t.lines = append(t.lines, transcriptLine{id, role, text})
}
func (t *callTranscript) text() string {
	var lines []string
	for _, l := range t.lines {
		if l.text == "" {
			continue
		}
		role := "assistant"
		if l.role == "owner" {
			role = "user"
		}
		lines = append(lines, role+": "+l.text)
	}
	return strings.Join(lines, "\n")
}
func (t *callTranscript) end() string {
	for _, l := range t.lines {
		if l.role == "owner" && strings.TrimSpace(l.text) != "" {
			return delegation("The user just ended their realtime session. Here is the remaining handoff/transcript tail. You probably do not have to do anything; acknowledge the handoff unless the transcript itself asks for something.", t.text(), true)
		}
	}
	return ""
}

// delegation ports RealtimeDelegation::render, including its field bounds and
// XML escaping. The websocket protocol uses Codex's bounded (V1) rendering.
func delegation(input, transcript string, ended bool) string {
	escape := func(s string) string { return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s) }
	input = escape(input)
	if len(input) > 4096 {
		input = head(input, 4093) + "…"
	}
	out := "<realtime_delegation>\n"
	if ended {
		out += "  <source>transcript_tail_flush</source>\n"
	}
	out += "  <input>" + input + "</input>\n"
	if transcript != "" {
		transcript = escape(transcript)
		if len(transcript) > 4096 {
			transcript = "…" + tail(transcript, 4093)
		}
		out += "  <transcript_delta>" + transcript + "</transcript_delta>\n"
	}
	return out + "</realtime_delegation>"
}

func startupItem(history string) message {
	if history == "" {
		return nil
	}
	const header = "<startup_context>\nStartup context from Dyna.\nThis is background context about recent work. It may be incomplete or stale. Use it to inform responses, and do not repeat it back unless relevant.\n\nRecent conversation with root:\n"
	const footer = "\n</startup_context>"
	const marker = "\n…output truncated…\n"
	const limit = 20000
	if len(header)+len(history)+len(footer) > limit {
		history = marker + tail(history, limit-len(header)-len(footer)-len(marker))
	}
	return message{"type": "conversation.item.create", "item": message{"type": "message", "role": "developer", "content": []message{{"type": "input_text", "text": header + history + footer}}}}
}
