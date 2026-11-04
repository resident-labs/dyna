package pig

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"

	"github.com/resident-labs/dyna/contract"
)

// EndPost is what a tool records in its result's details when its post ends the turn.
type EndPost struct {
	EndPost contract.EventID `json:"endPost,omitempty"`
}

// turn is what a session's events build up while one turn runs.
type turn struct {
	id       contract.TurnID
	activity func(contract.TurnID, contract.ActivityKind, string)
	result   contract.TurnEnded
	batch    endBatch
	reply    replyText
}

func newTurn(id contract.TurnID, activity func(contract.TurnID, contract.ActivityKind, string)) *turn {
	return &turn{id: id, activity: activity, result: contract.TurnEnded{Turn: id, Outcome: contract.TurnCompleted}}
}

func (t *turn) event(e agent.AgentEvent) {
	switch e := e.(type) {
	case agent.ToolExecutionStartEvent:
		t.activity(t.id, contract.ActivityTool, activityLabel(e.ToolName, e.Args))
	case agent.CompactionStartEvent:
		// A compaction runs a model call with no tool, so the line says so rather than keep the last command.
		t.activity(t.id, contract.ActivityCompaction, "compacting the conversation")
	case agent.MessageEndEvent:
		t.message(e.Message)
	case agent.AgentSettledEvent:
		if e.Aborted {
			t.result.Outcome, t.result.Error, t.result.Cause = contract.TurnStopped, "", ""
		} else if t.result.Outcome == contract.TurnCompleted {
			t.batch.apply(&t.result)
		}
	}
}

func (t *turn) message(m agent.AgentMessage) {
	switch {
	case m.User != nil:
		t.batch = endBatch{}
		t.reply.steered()
	case m.ToolResult != nil:
		t.batch.toolResult(m.ToolResult.Details, m.ToolResult.IsError)
	case m.Assistant != nil:
		a := m.Assistant.Observe()
		t.batch.assistant(a.Content)
		t.result.Final = t.reply.assistant(a.Content)
		t.result.Usage.Model = a.ModelID
		if a.ResponseModel != "" {
			t.result.Usage.Model = a.ResponseModel
		}
		if a.Usage != nil {
			t.result.Usage.InputTokens += int64(a.Usage.Input)
			t.result.Usage.OutputTokens += int64(a.Usage.Output)
			t.result.Usage.CacheReadTokens += int64(a.Usage.CacheRead)
			t.result.Usage.CacheWriteTokens += int64(a.Usage.CacheWrite)
		}
		switch a.StopReason {
		case ai.StopReasonError:
			t.result.Outcome, t.result.Error, t.result.Cause = contract.TurnFailed, a.ErrorMessage, contract.CauseModelFailed
		case ai.StopReasonLength:
			t.result.Outcome, t.result.Error, t.result.Cause = contract.TurnFailed, a.ErrorMessage, contract.CauseOutputLimit
		case ai.StopReasonAborted:
			t.result.Outcome, t.result.Error, t.result.Cause = contract.TurnStopped, "", ""
		default:
			t.result.Outcome, t.result.Error, t.result.Cause = contract.TurnCompleted, "", ""
		}
	}
}

// endBatch finds whether the latest tool batch ended the turn. The agent stops only when every call in the batch ends
// it, so each call must have reported its post.
type endBatch struct {
	calls int
	posts []contract.EventID
}

func (b *endBatch) assistant(content []ai.AssistantContentBlock) {
	*b = endBatch{}
	for _, block := range content {
		if _, ok := block.(ai.ToolCall); ok {
			b.calls++
		}
	}
}

// toolResult reads details through JSON: details belong to each tool and can be any value, and only an end post counts.
func (b *endBatch) toolResult(details any, failed bool) {
	data, err := json.Marshal(details)
	var end EndPost
	if err == nil && json.Unmarshal(data, &end) == nil && end.EndPost != "" && !failed {
		b.posts = append(b.posts, end.EndPost)
	}
}

// apply makes the posts the turn's reply when the batch ended the turn. Text beside the last tool call was working
// text, so the turn has no final response.
func (b *endBatch) apply(result *contract.TurnEnded) bool {
	if b.calls == 0 || len(b.posts) != b.calls {
		return false
	}
	result.Final, result.EndPosts = "", b.posts
	return true
}

// replyText builds a turn's final text. A steer is read as soon as the agent stops, inside the same turn, so the next
// message must not replace what already answered an earlier input.
type replyText struct {
	kept, closing string
}

// assistant returns the final text so far. Only a message with no tool call can close an answer.
func (r *replyText) assistant(content []ai.AssistantContentBlock) string {
	var text []string
	r.closing = ""
	working := false
	for _, block := range content {
		switch block := block.(type) {
		case ai.TextContent:
			text = append(text, block.Text)
		case ai.ToolCall:
			working = true
		}
	}
	current := strings.Join(text, "\n")
	if !working {
		r.closing = current
	}
	return joinReply(r.kept, current)
}

// steered keeps the answer a new input landed after.
func (r *replyText) steered() {
	r.kept, r.closing = joinReply(r.kept, r.closing), ""
}

func joinReply(kept, text string) string {
	if kept == "" || text == "" {
		return kept + text
	}
	return kept + "\n\n" + text
}

// activityLabel is the line the owner sees while a tool runs: its name and the argument that says what it is doing.
func activityLabel(tool string, raw json.RawMessage) string {
	label := tool
	var args map[string]json.RawMessage
	_ = json.Unmarshal(raw, &args)
	var host string
	if tool == "bash" && json.Unmarshal(args["host"], &host) == nil && host != "" {
		label += " on " + host + ":"
	}
	// A computer action's status is what it is for, which the owner watching reads under the screen.
	for _, key := range []string{"status", "path", "command", "pattern", "query", "url", "job"} {
		var value string
		if json.Unmarshal(args[key], &value) != nil || value == "" {
			continue
		}
		if key == "path" {
			value = filepath.Base(value)
		}
		label += " " + value
		break
	}
	label, _, _ = strings.Cut(label, "\n")
	return strings.ReplaceAll(label, "\r", " ")
}
