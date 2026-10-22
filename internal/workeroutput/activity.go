package workeroutput

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// activityTail bounds how much of a worker's output LastActivity reads.
const activityTail = 16 << 10

// Activity is what a worker is doing now. Since is when its running tool
// call or compaction started (unix ms); it is zero when nothing is running,
// for example after a tool finished.
type Activity struct {
	Text  string `json:"text,omitempty"`
	Since int64  `json:"since,omitempty"`
}

const compactingText = "Compacting context…"

// LastActivity names what a worker is doing now, from the end of its
// normalized output: a running compaction, else the newest tool call (with
// its input's description or first line), else Thinking or Writing. It
// reads only the file's tail, so it stays cheap on every refresh; a torn or
// partial line is skipped. A missing or empty output gives no activity.
func LastActivity(path string) Activity {
	f, err := os.Open(path)
	if err != nil {
		return Activity{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Activity{}
	}
	start := max(info.Size()-activityTail, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return Activity{}
	}
	lines := bytes.Split(buf, []byte("\n"))
	ended := map[string]bool{}
	compactionEnded := false
	// running is the newest tool call that has only progress records so far
	// (Codex item.updated, OpenCode running); its start time is further back.
	var running *Activity
	runningID := ""
	for i := len(lines) - 1; i >= 0; i-- {
		var e Event
		if json.Unmarshal(lines[i], &e) != nil {
			continue
		}
		finished := e.State == "completed" || e.State == "failed"
		switch e.Kind {
		case "status":
			if running != nil || e.Name != "compaction" {
				continue
			}
			if finished {
				compactionEnded = true
			} else if !compactionEnded {
				return Activity{Text: compactingText, Since: e.TS}
			}
		case "tool":
			if finished && e.ID != "" {
				ended[e.ID] = true
			}
			if e.Name == "tool_result" {
				continue
			}
			if running != nil {
				if e.ID == runningID && e.State == "started" {
					running.Since = e.TS
					return *running
				}
				continue
			}
			if !finished && e.State != "started" && e.ID != "" && !ended[e.ID] {
				running, runningID = &Activity{Text: toolLine(e), Since: e.TS}, e.ID
				continue
			}
			a := Activity{Text: toolLine(e)}
			if e.State == "started" && !ended[e.ID] {
				a.Since = e.TS
			}
			return a
		case "reasoning", "message":
			if running != nil {
				continue
			}
			if e.Kind == "reasoning" {
				return Activity{Text: "Thinking"}
			}
			return Activity{Text: "Writing"}
		}
	}
	if running != nil {
		return *running
	}
	return Activity{}
}

// toolLine is a tool call in one line: its name and its input's description
// (as Claude's Bash gives one), else the first line of its input.
func toolLine(e Event) string {
	summary := ""
	var input struct {
		Description string `json:"description"`
	}
	if json.Unmarshal([]byte(e.Input), &input) == nil && strings.TrimSpace(input.Description) != "" {
		summary = strings.TrimSpace(input.Description)
	} else {
		summary, _, _ = strings.Cut(strings.TrimSpace(e.Input), "\n")
	}
	if r := []rune(summary); len(r) > 100 {
		summary = string(r[:100]) + "…"
	}
	if summary == "" {
		return e.Name
	}
	return e.Name + ": " + summary
}
