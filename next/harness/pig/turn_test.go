package pig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"

	"github.com/resident-labs/dyna/contract"
)

func assistant(stop ai.StopReason, blocks ...ai.AssistantContentBlock) agent.AgentEvent {
	return agent.MessageEndEvent{Message: agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: "assistant", Content: blocks, StopReason: stop, ModelID: "m", Usage: &ai.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}}}}
}

func toolResult(id string, details any, failed bool) agent.AgentEvent {
	return agent.MessageEndEvent{Message: agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Role: "toolResult", ToolCallID: id, Details: details, IsError: failed}}}
}

var (
	answer      = assistant(ai.StopReasonStop, ai.TextContent{Text: "answer"})
	narrated    = assistant(ai.StopReasonToolUse, ai.TextContent{Text: "Sending it."}, ai.ToolCall{ID: "a"})
	twoCalls    = assistant(ai.StopReasonToolUse, ai.ToolCall{ID: "a"}, ai.ToolCall{ID: "b"})
	endResult   = toolResult("a", map[string]any{"endPost": "post_a"}, false)
	endResult2  = toolResult("b", EndPost{EndPost: "post_b"}, false)
	plainResult = toolResult("b", map[string]any{}, false)
	failedPost  = toolResult("a", map[string]any{"endPost": "post_a"}, true)
	steered     = agent.MessageEndEvent{Message: agent.AgentMessage{User: &agent.UserMessage{Role: "user", Content: ai.UserText("more")}}}
	settled     = agent.AgentSettledEvent{}
)

func replay(events ...agent.AgentEvent) contract.TurnEnded {
	t := newTurn("turn_a", func(contract.TurnID, contract.ActivityKind, string) {})
	for _, e := range events {
		t.event(e)
	}
	return t.result
}

// A steer that lands once the agent stopped runs in the same turn, and the answer to the earlier input must survive it.
func TestAnswerBeforeASteerStaysInTheFinalText(t *testing.T) {
	noted := assistant(ai.StopReasonStop, ai.TextContent{Text: "noted"})
	if got := replay(answer, steered, noted, settled); got.Final != "answer\n\nnoted" {
		t.Fatalf("result %+v", got)
	}
	if got := replay(narrated, plainResult, steered, answer, settled); got.Final != "answer" {
		t.Fatalf("working text was kept: %+v", got)
	}
}

func TestEndPostEndsTheTurnWithoutFinalText(t *testing.T) {
	got := replay(narrated, endResult, settled)
	if got.Outcome != contract.TurnCompleted || got.Final != "" || !reflect.DeepEqual(got.EndPosts, []contract.EventID{"post_a"}) {
		t.Fatalf("result %+v", got)
	}
}

func TestEveryPostOfAnEndBatchIsReported(t *testing.T) {
	got := replay(twoCalls, endResult, endResult2, settled)
	if !reflect.DeepEqual(got.EndPosts, []contract.EventID{"post_a", "post_b"}) {
		t.Fatalf("result %+v", got)
	}
}

// Details belong to each tool, so a value of another shape must not count as a post or stop the turn.
func TestToolDetailsOfAnyShape(t *testing.T) {
	for _, details := range []any{"text", []int{1, 2}, 7, map[string]any{"endPost": 7}, nil} {
		got := replay(narrated, toolResult("a", details, false), answer, settled)
		if got.Final != "answer" || got.EndPosts != nil {
			t.Fatalf("details %v: result %+v", details, got)
		}
	}
}

// The agent keeps going after these batches, so the turn ends on text as usual.
func TestTurnsThatContinuePastAnEndPost(t *testing.T) {
	for name, events := range map[string][]agent.AgentEvent{
		"batch with another tool": {twoCalls, endResult, plainResult, answer, settled},
		"failed post":             {narrated, failedPost, answer, settled},
		"steering":                {narrated, endResult, steered, answer, settled},
	} {
		t.Run(name, func(t *testing.T) {
			if got := replay(events...); got.Final != "answer" || got.EndPosts != nil {
				t.Fatalf("result %+v", got)
			}
		})
	}
}

// The harness reads the batch itself rather than trust that the agent stopped only after an end batch.
func TestABatchThatDidNotEndTheTurnReportsNoEndPost(t *testing.T) {
	for name, events := range map[string][]agent.AgentEvent{
		"post beside another tool": {twoCalls, endResult, plainResult, settled},
		"failed post":              {narrated, failedPost, settled},
	} {
		t.Run(name, func(t *testing.T) {
			if got := replay(events...); got.EndPosts != nil {
				t.Fatalf("result %+v", got)
			}
		})
	}
}

func TestStoppedTurnReportsNoEndPost(t *testing.T) {
	got := replay(narrated, endResult, agent.AgentSettledEvent{Aborted: true})
	if got.Outcome != contract.TurnStopped || got.EndPosts != nil {
		t.Fatalf("result %+v", got)
	}
}

func TestStopReasonsSetTheOutcome(t *testing.T) {
	failed := assistant(ai.StopReasonError)
	failed.(agent.MessageEndEvent).Message.Assistant.ErrorMessage = "overloaded"
	for name, tc := range map[string]struct {
		events []agent.AgentEvent
		want   contract.TurnEnded
	}{
		"error":         {[]agent.AgentEvent{failed, settled}, contract.TurnEnded{Outcome: contract.TurnFailed, Error: "overloaded", Cause: contract.CauseModelFailed}},
		"length":        {[]agent.AgentEvent{assistant(ai.StopReasonLength), settled}, contract.TurnEnded{Outcome: contract.TurnFailed, Cause: contract.CauseOutputLimit}},
		"aborted":       {[]agent.AgentEvent{assistant(ai.StopReasonAborted), settled}, contract.TurnEnded{Outcome: contract.TurnStopped}},
		"retry clears":  {[]agent.AgentEvent{failed, answer, settled}, contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "answer"}},
		"abort settles": {[]agent.AgentEvent{failed, agent.AgentSettledEvent{Aborted: true}}, contract.TurnEnded{Outcome: contract.TurnStopped}},
	} {
		t.Run(name, func(t *testing.T) {
			got := replay(tc.events...)
			if got.Outcome != tc.want.Outcome || got.Error != tc.want.Error || got.Cause != tc.want.Cause || got.Final != tc.want.Final {
				t.Fatalf("result %+v", got)
			}
		})
	}
}

func TestUsageAddsUpAndNamesTheResponseModel(t *testing.T) {
	routed := assistant(ai.StopReasonStop, ai.TextContent{Text: "answer"})
	routed.(agent.MessageEndEvent).Message.Assistant.ResponseModel = "m-2026"
	got := replay(narrated, plainResult, routed, settled)
	if got.Usage != (contract.Usage{Model: "m-2026", InputTokens: 2, OutputTokens: 4, CacheReadTokens: 6, CacheWriteTokens: 8}) {
		t.Fatalf("usage %+v", got.Usage)
	}
}

func TestToolsAndCompactionShowOnTheActivityLine(t *testing.T) {
	type line struct {
		kind  contract.ActivityKind
		label string
	}
	var got []line
	tr := newTurn("turn_a", func(_ contract.TurnID, kind contract.ActivityKind, label string) {
		got = append(got, line{kind, label})
	})
	tr.event(agent.ToolExecutionStartEvent{ToolName: "bash", Args: json.RawMessage(`{"command":"go test"}`)})
	tr.event(agent.CompactionStartEvent{Reason: "threshold"})
	want := []line{{contract.ActivityTool, "bash go test"}, {contract.ActivityCompaction, "compacting the conversation"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestActivityLabel(t *testing.T) {
	for _, tc := range []struct{ name, args, want string }{
		{"read", `{"path":"/a/spec.md"}`, "read spec.md"},
		{"bash", `{"command":"` + strings.Repeat("x", 90) + `\nignored"}`, "bash " + strings.Repeat("x", 90)},
		{"bash", `{"command":"uptime","host":"hermeslab"}`, "bash on hermeslab: uptime"},
		{"job", `{"action":"output","job":"job_1"}`, "job job_1"},
		{"computer", `{"action":"click","x":10,"y":20,"status":"Opening the settings"}`, "computer Opening the settings"},
		{"computer", `{"action":"launch","command":"urxvt"}`, "computer urxvt"},
		{"tool", `{"other":"value"}`, "tool"},
		{"tool", `["not an object"]`, "tool"},
	} {
		if got := activityLabel(tc.name, json.RawMessage(tc.args)); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
