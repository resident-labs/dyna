package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func newPiGCrew(t *testing.T, model *pigtest.Model, extensions ...extension.InlineExtension) *crewHarness {
	t.Helper()
	h := newCrewHarness(t)
	state := pigtest.AgentDir(t, model, "")
	h.open = func(ctx context.Context, _ contract.WorkflowProfile, _, dir string, document json.RawMessage, schema *jsonschema.Schema) (crewWorker, error) {
		c := pig.Config{Dir: dir, AgentDir: state, SessionDir: filepath.Join(state, "sessions"), New: true,
			Model:        contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model"},
			Instructions: crewInstructions("test core", document, "", dir), Extensions: extensions}
		if schema != nil {
			c.ResultTool = structuredOutput(schema)
		}
		return pig.Open(ctx, c)
	}
	return h
}

func runWithSteer(t *testing.T, model *pigtest.Model, script string, wantDelivered bool) contract.TurnEnded {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	requested, release := make(chan struct{}), make(chan struct{})
	model.BeforeResponse = func(n int) {
		if n == 1 {
			close(requested)
			select {
			case <-ctx.Done():
			case <-release:
			}
		}
	}
	crew, ended := newPiGCrew(t, model).start(script)
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("worker never requested its first response")
	}
	if err := crew.Steer(ctx, contract.TurnInput{Delivery: 8, Text: "A background job finished."}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case result := <-ended:
		delivered, err := crew.Delivered(ctx, 8)
		if err != nil || delivered != wantDelivered {
			t.Fatalf("steer delivered = %v, want %v: %v", delivered, wantDelivered, err)
		}
		return result
	case <-ctx.Done():
		t.Fatal("worker never finished")
		return contract.TurnEnded{}
	}
}

func TestAStructuredOutputCallEndsTheStepDespiteAQueuedSteer(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{
		{Text: `{"count":0}`},
		{Tool: "StructuredOutput", Input: `{"result":{"count":1}}`},
		{Tool: "StructuredOutput", Input: `{"result":{"count":2}}`},
		{Tool: "read", Input: `{"path":"not-found"}`},
	}, []pigtest.Block{{Text: `{"count":99}`}})
	result := runWithSteer(t, model, crewMeta+`return await agent('count them', `+countSchema+`)`, false)
	if result.Outcome != contract.TurnCompleted || result.Final != `{"count":1}` || len(model.Requests()) != 1 {
		t.Fatalf("result %+v, %d model requests", result, len(model.Requests()))
	}
}

func TestAStructuredOutputCallSkipsExtensionSettleContinuation(t *testing.T) {
	model := pigtest.NewModel(
		[]pigtest.Block{{Tool: "StructuredOutput", Input: `{"result":{"count":1}}`}},
		[]pigtest.Block{{Tool: "StructuredOutput", Input: `{"result":{"count":2}}`}},
	)
	settled := 0
	continueOnce := extension.NamedInlineExtension{Name: "once-more", Factory: func(api extension.API) error {
		api.OnAgentBeforeSettle(func(context.Context, *extension.AgentBeforeSettleEvent) (extension.AgentBeforeSettleEventResult, error) {
			settled++
			if settled > 1 {
				return extension.AgentBeforeSettleEventResult{}, nil
			}
			entries := []extension.SessionBoundaryDraft{{Type: "custom_message", CustomType: "once-more", Content: "Reply again."}}
			return extension.AgentBeforeSettleEventResult{Entries: &entries, Continue: new(true)}, nil
		})
		return nil
	}}
	got := newPiGCrew(t, model, continueOnce).result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
	if got["count"] != float64(1) || len(model.Requests()) != 1 || settled != 0 {
		t.Fatalf("result %v after %d model requests, %d settle hooks", got, len(model.Requests()), settled)
	}
}

func TestAnInvalidStructuredOutputCallReturnsAToolErrorOnTheSameTurn(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Tool: "StructuredOutput", Input: `{"result":0}`}},
		[]pigtest.Block{{Tool: "StructuredOutput", Input: `{"result":7}`}})
	h := newPiGCrew(t, model)
	// A scalar result with a local reference and a conditional is valid for the workflow, not a provider tool's input.
	got := h.result(crewMeta + `return { result: await agent('pick a positive count', {schema: {
		$defs: {count: {type: 'integer', minimum: 1}}, if: {type: 'number'}, then: {$ref: '#/$defs/count'}, else: false
	}}) }`).(map[string]any)
	requests := model.Requests()
	if got["result"] != float64(7) || len(requests) != 2 {
		t.Fatalf("result %v after %d model requests", got, len(requests))
	}
	found := false
	for _, raw := range requests[0].Tools {
		var tool struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatal(err)
		}
		if tool.Name == "StructuredOutput" {
			found = true
			if !strings.Contains(string(tool.Input), `"result":{}`) || strings.Contains(string(tool.Input), `"$defs"`) {
				t.Fatalf("tool's parameter schema was %s", tool.Input)
			}
		}
	}
	last := string(requests[1].Messages[len(requests[1].Messages)-1])
	if !found || !strings.Contains(last, `"is_error":true`) || !strings.Contains(last, "minimum") {
		t.Fatalf("tool found %v; after the first call the model saw %s", found, last)
	}
}

func TestTheTextFallbackChecksClosingMessagesIndividually(t *testing.T) {
	for _, tc := range []struct{ name, second, want string }{
		{"latest valid", `{"count":2}`, `{"count":2}`},
		{"latest invalid", `{"status":"plan","steps":[]}`, `{"count":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := pigtest.NewModel([]pigtest.Block{{Text: `{"count":1}`}}, []pigtest.Block{{Text: tc.second}})
			result := runWithSteer(t, model, crewMeta+`return await agent('count them', `+countSchema+`)`, true)
			requests := model.Requests()
			if result.Outcome != contract.TurnCompleted || result.Final != tc.want || len(requests) != 2 {
				t.Fatalf("result %+v, %d model requests", result, len(requests))
			}
		})
	}
}

func TestAPlainTextWorkerDoesNotGetStructuredOutput(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "plain text"}})
	got := newPiGCrew(t, model).result(crewMeta + `return await agent('say something')`)
	if got != "plain text" || strings.Contains(string(model.Requests()[0].System), "call StructuredOutput") {
		t.Fatalf("result was %v, prompt was %s", got, model.Requests()[0].System)
	}
	for _, tool := range model.Requests()[0].Tools {
		if strings.Contains(string(tool), `"StructuredOutput"`) {
			t.Fatalf("plain worker was offered %s", tool)
		}
	}
}
