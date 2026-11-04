package pig

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// Lines of a session file as PiG writes them.
const (
	recordedUser     = `{"type":"message","id":"a","message":{"role":"user","content":[{"type":"text","text":"go"}],"timestamp":1,"deliveryId":"9"}}`
	recordedEarlier  = `{"type":"message","id":"0","message":{"role":"user","content":[{"type":"text","text":"before"}],"timestamp":1,"deliveryId":"8"}}`
	recordedOld      = `{"type":"message","id":"1","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1}}`
	recordedAnswer   = `{"type":"message","id":"b","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":3,"output":4,"cacheRead":0,"cacheWrite":0,"totalTokens":7,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}}`
	recordedNarrated = `{"type":"message","id":"c","message":{"role":"assistant","content":[{"type":"text","text":"Sending."},{"type":"toolCall","id":"t1","name":"mcp__dyna__post","arguments":{}}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":2}}`
	recordedTwoCalls = `{"type":"message","id":"c","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"mcp__dyna__post","arguments":{}},{"type":"toolCall","id":"t2","name":"bash","arguments":{}}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":2}}`
	recordedEnd      = `{"type":"message","id":"d","message":{"role":"toolResult","toolCallId":"t1","toolName":"mcp__dyna__post","content":[{"type":"text","text":"ok"}],"details":{"server":"dyna","tool":"post","endPost":"post_a"},"isError":false,"timestamp":3}}`
	recordedFailed   = `{"type":"message","id":"d","message":{"role":"toolResult","toolCallId":"t1","toolName":"mcp__dyna__post","content":[{"type":"text","text":"no"}],"details":{"server":"dyna","tool":"post","endPost":"post_a"},"isError":true,"timestamp":3}}`
	recordedPlain    = `{"type":"message","id":"e","message":{"role":"toolResult","toolCallId":"t2","toolName":"bash","content":[{"type":"text","text":"ok"}],"details":{},"isError":false,"timestamp":3}}`
	recordedSteer    = `{"type":"message","id":"f","message":{"role":"user","content":[{"type":"text","text":"more"}],"timestamp":4,"deliveryId":"10"}}`
	// recordedLabelled is an answer that carries the delivery member of the turn's input, as only a user message should.
	recordedLabelled = `{"type":"message","id":"g","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2,"deliveryId":"9"}}`
)

func recoverLines(t *testing.T, lines ...string) (contract.TurnEnded, bool) {
	t.Helper()
	return recoverFrom(t, 9, lines...)
}

// recoverFrom recovers the turn whose first input is the delivery first.
func recoverFrom(t *testing.T, first contract.DeliverySeq, lines ...string) (contract.TurnEnded, bool) {
	t.Helper()
	result, done, err := recoverTurn(strings.NewReader(strings.Join(lines, "\n")+"\n"), "turn_a", first)
	if err != nil {
		t.Fatal(err)
	}
	return result, done
}

func TestRecoveryOfAnAnsweredTurn(t *testing.T) {
	result, done := recoverLines(t, recordedEarlier, recordedOld, recordedUser, recordedAnswer)
	if !done || result.Outcome != contract.TurnCompleted || result.Final != "answer" || result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 4 {
		t.Fatalf("done=%v result=%+v", done, result)
	}
}

func TestRecoveryOfATurnEndedByPost(t *testing.T) {
	result, done := recoverLines(t, recordedUser, recordedNarrated, recordedEnd)
	if !done || result.Final != "" || !reflect.DeepEqual(result.EndPosts, []contract.EventID{"post_a"}) {
		t.Fatalf("done=%v result=%+v", done, result)
	}
}

func TestRecoveryContinuesAnUnfinishedTurn(t *testing.T) {
	for name, lines := range map[string][]string{
		"not started":      {recordedEarlier, recordedOld},
		"no answer yet":    {recordedUser},
		"tool running":     {recordedUser, recordedNarrated},
		"sibling tool":     {recordedUser, recordedTwoCalls, recordedEnd, recordedPlain},
		"failed post":      {recordedUser, recordedNarrated, recordedFailed},
		"steered":          {recordedUser, recordedNarrated, recordedEnd, recordedSteer},
		"answer then more": {recordedUser, recordedAnswer, recordedSteer},
		"broken line":      {recordedUser, recordedAnswer[:40]},
		"labelled answer":  {recordedLabelled},
	} {
		t.Run(name, func(t *testing.T) {
			if _, done := recoverLines(t, lines...); done {
				t.Fatal("finished")
			}
		})
	}
}

// Every input of a turn is recorded with its delivery, so the runner knows which ones the session already read.
func TestTheSessionFileRecordsEachDelivery(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "both"}})
	h := open(t, model)
	if done, err := h.Delivered(context.Background(), 1); err != nil || done {
		t.Fatalf("before the turn: %v %v", done, err)
	}
	start(t, h, "first", "second")
	for seq, want := range map[contract.DeliverySeq]bool{1: true, 2: true, 3: false} {
		if done, err := h.Delivered(context.Background(), seq); err != nil || done != want {
			t.Errorf("delivery %d: %v %v", seq, done, err)
		}
	}
	for _, message := range model.Requests()[0].Messages {
		if strings.Contains(string(message), "deliveryId") {
			t.Fatalf("the model was sent %s", message)
		}
	}
	result, done, err := h.Recover("turn_a", []contract.TurnInput{{Delivery: 1}})
	if err != nil || !done || result.Final != "both" {
		t.Fatalf("recover: %+v %v %v", result, done, err)
	}
}

// A box agent that stopped during a tool call left its turn in the session file. The new one finishes that turn without
// running the call again or sending its input twice.
func TestContinueFinishesATurnLeftRunning(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Tool: "hang", Input: `{}`}}, []pigtest.Block{{Text: "recovered"}})
	c := config(t, model, "")
	running := make(chan struct{})
	c.Extensions = []extension.InlineExtension{extension.NamedInlineExtension{Name: "test-hang", Factory: func(api extension.API) error {
		api.RegisterTool(extension.ToolDefinition{
			Name: "hang", Label: "hang", Description: "Never returns.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
			Execute: func(ctx context.Context, _ string, _ json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
				close(running)
				<-ctx.Done()
				return extension.AgentToolResult{}, ctx.Err()
			},
		})
		return nil
	}}}
	stopped, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stopped.Close() })
	go func() {
		_, _ = stopped.Start(context.Background(), "turn_a", []contract.TurnInput{{Delivery: 9, Text: "go"}}, func() {})
	}()
	<-running
	c.Extensions = nil
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, done, err := h.Recover("turn_a", []contract.TurnInput{{Delivery: 9}}); err != nil || done {
		t.Fatalf("recover: %v %v", done, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := false
	result, err := h.Continue(ctx, "turn_a", func() { started = true })
	if err != nil || !started || result.Final != "recovered" {
		t.Fatalf("continue: %+v %v started=%v", result, err, started)
	}
	requests := model.Requests()
	sent := requests[len(requests)-1].Messages
	if len(requests) != 2 || len(sent) != 3 || !strings.Contains(string(sent[2]), "interrupted: the session restarted") {
		t.Fatalf("%d requests; the last sent %s", len(requests), sent)
	}
}

// A turn is found by its own first input: another delivery's answered turn is not it.
func TestRecoveryFindsTheTurnByItsFirstInput(t *testing.T) {
	if _, done := recoverFrom(t, 10, recordedUser, recordedAnswer); done {
		t.Fatal("the turn of delivery 9 finished the turn of delivery 10")
	}
	if result, done := recoverFrom(t, 10, recordedUser, recordedAnswer, recordedSteer, recordedAnswer); !done || result.Final != "answer" {
		t.Fatalf("done=%v result=%+v", done, result)
	}
}

// A steer and a follow-up are recorded with their deliveries too.
func TestSteerAndFollowUpRecordTheirDeliveries(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Tool: "queue", Input: `{}`}}, []pigtest.Block{{Text: "steered"}}, []pigtest.Block{{Text: "followed"}})
	c := config(t, model, "")
	var h *Harness
	c.Extensions = []extension.InlineExtension{extension.NamedInlineExtension{Name: "test-queue", Factory: func(api extension.API) error {
		api.RegisterTool(extension.ToolDefinition{
			Name: "queue", Label: "queue", Description: "Queues two inputs.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
			Execute: func(ctx context.Context, _ string, _ json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
				return extension.AgentToolResult{}, errors.Join(h.Steer(ctx, contract.TurnInput{Delivery: 5, Text: "steer"}), h.FollowUp(ctx, contract.TurnInput{Delivery: 6, Text: "follow"}))
			},
		})
		return nil
	}}}
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	start(t, h, "go")
	for _, seq := range []contract.DeliverySeq{5, 6} {
		if done, err := h.Delivered(context.Background(), seq); err != nil || !done {
			t.Errorf("delivery %d: %v %v", seq, done, err)
		}
	}
}
