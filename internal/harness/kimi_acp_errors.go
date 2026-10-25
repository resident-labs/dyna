package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/providerfailure"
)

// TODO: legacy/shim remove when Kimi reports failed turns through ACP instead
// of end_turn. Closing the Kimi 2.3.1 native session flushes turn.ended, so its
// journal preserves failures that the protocol currently hides.
type kimiPromptJournal struct {
	home    string
	session string
	offsets map[string]int64
}

type kimiTurnError struct {
	Code    string
	Message string
}

func (e *kimiTurnError) Error() string {
	if e.Code == "loop.max_steps_exceeded" {
		return "kimi-code turn stopped: step limit"
	}
	return fmt.Sprintf("kimi-code turn failed (%s): %s", e.Code, e.Message)
}

// kimiFailure tags a native Kimi turn failure with the reason its own code
// names. A provider connection error is the provider's failure; a step limit is
// Kimi's own limit, not the provider's, so it stays untagged. The tag wraps the
// error, so errors.As still finds the native failure.
func kimiFailure(failure *kimiTurnError) error {
	if failure.Code == "provider.connection_error" {
		return providerfailure.Tag(providerfailure.Unavailable, failure)
	}
	return failure
}

func newKimiPromptJournal(home, session string) kimiPromptJournal {
	j := kimiPromptJournal{home: home, session: session, offsets: make(map[string]int64)}
	for _, path := range j.paths() {
		if info, err := os.Stat(path); err == nil {
			j.offsets[path] = info.Size()
		}
	}
	return j
}

func (j kimiPromptJournal) paths() []string {
	if j.home == "" || j.session == "" || j.session == "." || j.session == ".." || filepath.Base(j.session) != j.session {
		return nil
	}
	root := filepath.Join(j.home, "sessions")
	workspaces, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var paths []string
	for _, workspace := range workspaces {
		if workspace.IsDir() {
			paths = append(paths, filepath.Join(root, workspace.Name(), j.session, "agents", "main", "wire.jsonl"))
		}
	}
	return paths
}

func (j kimiPromptJournal) promptError() error {
	for _, path := range j.paths() {
		if err := kimiJournalError(path, j.offsets[path]); err != nil {
			return err
		}
	}
	return nil
}

func kimiJournalError(path string, offset int64) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return nil
	}
	// Only the current prompt and its last 64 KiB can supply a terminal event.
	// Tool results may be much larger, so discard the first partial record.
	start := max(offset, info.Size()-(64<<10))
	data := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(data, start); err != nil {
		return nil
	}
	if start > offset {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			return nil
		}
		data = data[end+1:]
	}
	var result error
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var event struct {
			Type    string `json:"type"`
			AgentID string `json:"agentId"`
			Reason  string `json:"reason"`
			Error   *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "turn.ended" || event.AgentID != "main" {
			continue
		}
		result = nil
		if event.Reason == "failed" {
			failure := &kimiTurnError{Message: "The native turn failed."}
			if event.Error != nil {
				failure.Code, failure.Message = event.Error.Code, event.Error.Message
			}
			result = kimiFailure(failure)
		}
	}
	return result
}
