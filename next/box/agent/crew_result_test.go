package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

const countSchema = `{ schema: { type: 'object', required: ['count'], properties: { count: { type: 'number' } } } }`

// answers makes a harness whose workers answer with each text in turn, and records the prompt of every turn.
func answers(h *crewHarness, replies ...contract.TurnEnded) *[]string {
	var mu sync.Mutex
	var prompts []string
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		return replies[min(len(prompts), len(replies))-1], nil
	}
	return &prompts
}

func text(final string) contract.TurnEnded {
	return contract.TurnEnded{Final: final, ClosingTexts: []string{final}, LastText: final}
}

func TestASchemaStepsFinalTextIsParsedAsItsResult(t *testing.T) {
	for name, final := range map[string]string{
		"bare":       `{"count": 3}`,
		"padded":     "\n {\"count\": 3}\n",
		"fenced":     "```json\n{\"count\": 3}\n```",
		"prose":      `Here you go: {"count": 3}`,
		"two blocks": "```\n{\"count\": 1}\n```\n```\n{\"count\": 3}\n```",
	} {
		h := newCrewHarness(t)
		prompts := answers(h, text(final))
		got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
		if got["count"] != float64(3) || len(*prompts) != 1 || len(h.schemas) != 1 || !strings.Contains(h.schemas[0], `"count"`) {
			t.Errorf("%s: %v, %d turns, schemas %q", name, got, len(*prompts), h.schemas)
		}
	}
}

// A final text that is "Done.", invalid JSON or JSON of the wrong shape goes back to the same worker with the error.
func TestAMismatchComesBackToTheSameWorkerAndARetrySucceeds(t *testing.T) {
	for name, first := range map[string]string{
		"done":            "Done.",
		"invalid json":    `Here you go: {count: 3}`,
		"shape":           `{"count": "many"}`,
		"prose bad shape": `Here you go: {"count": "many"}`,
	} {
		h := newCrewHarness(t)
		prompts := answers(h, text(first), text(`{"count": 4}`))
		got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
		if got["count"] != float64(4) || len(*prompts) != 2 || h.started.Load() != 1 {
			t.Fatalf("%s: %v, %d turns, %d workers", name, got, len(*prompts), h.started.Load())
		}
		if follow := (*prompts)[1]; !strings.Contains(follow, "not accepted") || !strings.Contains(follow, "Call StructuredOutput") {
			t.Errorf("%s: the follow-up is %q", name, follow)
		}
	}
}

func TestThreeMismatchesFailTheStepWithTheLastText(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, text(`{"count":"many"}`), text("\nStill working.\n"), text("\nDone.\n"))
	script := crewMeta + `
		const step = () => agent('count them', ` + countSchema + `)
		try { await step() } catch (err) {
			const [failed] = await parallel([() => { throw err }])
			return { raw: err.rawText, failed }
		}
	`
	for range 2 { // A restarted box replays the stored failure, including the raw text.
		got := h.result(script).(map[string]any)
		failed := got["failed"].(map[string]any)
		if got["raw"] != "\nDone.\n" || failed["rawText"] != got["raw"] || failed["failed"] != true || len(*prompts) != schemaAttempts || strings.Contains(failed["reason"].(string), "Done.") {
			t.Fatalf("failure %+v after %d turns", got, len(*prompts))
		}
	}
	for _, step := range h.journal.steps {
		if step.State != contract.WorkflowStepFailed || step.RawText != "\nDone.\n" || step.Result != "" {
			t.Fatalf("journal %+v", step)
		}
	}
}

// A reply that reached the output limit is told the result is too large for text, and to write it to a file.
func TestANestedWorkflowPassesAlongAFailedWorkersRawText(t *testing.T) {
	h := newCrewHarness(t)
	answers(h, text("not JSON"))
	child := crewMeta + `return await agent('count them', ` + countSchema + `)`
	if err := os.WriteFile(filepath.Join(h.workdir, "child.js"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	got := h.result(crewMeta + `return await workflow('child.js').catch(err => err.rawText)`)
	if got != "not JSON" {
		t.Fatalf("raw text %q", got)
	}
}

func TestAFailedStepCapsRawTextWithoutCuttingARune(t *testing.T) {
	h := newCrewHarness(t)
	answers(h, text(strings.Repeat("x", stepResultBytes-1)+"érest"))
	got := h.result(crewMeta + `
		try { await agent('count them', ` + countSchema + `) }
		catch (err) { return err.rawText.length }
	`)
	if got != float64(stepResultBytes-1) {
		t.Fatalf("raw length %v", got)
	}
}

func TestATooLargeReplyIsToldToWriteAFile(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, contract.TurnEnded{Outcome: contract.TurnFailed, Cause: contract.CauseOutputLimit}, text(`{"count": 2}`))
	got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
	if got["count"] != float64(2) || len(*prompts) != 2 || !strings.Contains((*prompts)[1], "write it to a file") && !strings.Contains((*prompts)[1], "Write it to a file") {
		t.Fatalf("%v, prompts %q", got, *prompts)
	}
}

func TestTextFallbackKeepsAnEarlierCompleteReplyAfterOutputLimit(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, contract.TurnEnded{
		Outcome: contract.TurnFailed, Cause: contract.CauseOutputLimit,
		ClosingTexts: []string{`{"count":1}`}, LastText: `{"count":`,
	})
	got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
	if got["count"] != float64(1) || len(*prompts) != 1 {
		t.Fatalf("result %v after %d turns", got, len(*prompts))
	}
}

func TestAStepWithoutASchemaKeepsItsFinalTextAsIs(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, text("Done."))
	if got := h.result(crewMeta + `return await agent('plain')`); got != "Done." || len(*prompts) != 1 || fmt.Sprint(h.schemas) != "[]" && h.schemas[0] != "" {
		t.Fatalf("%v, prompts %q, schemas %q", got, *prompts, h.schemas)
	}
}

func TestReadResultEnforcesSizeLimitAndRejectsWrongTypes(t *testing.T) {
	schema, err := compileSchema([]byte(`{"type":"object","required":["count"]}`))
	if err != nil {
		t.Fatal(err)
	}
	const head, tail = `{"count":1,"pad":"`, `"}`
	for size, wantProblem := range map[int]bool{stepResultBytes: false, stepResultBytes + 1: true} {
		value, problem := readResult(schema, head+strings.Repeat("x", size-len(head)-len(tail))+tail)
		if (problem != "") != wantProblem || (value == nil) != wantProblem {
			t.Errorf("%d bytes: value %v, problem %q", size, value != nil, problem)
		}
	}
	for text, ok := range map[string]bool{"```\n{\"count\":1}\n```": true, "```json {\"count\":1}```": true, "ok ```{\"count\":1}```": true, "[1]": false, "": false} {
		if _, problem := readResult(schema, text); (problem == "") != ok {
			t.Errorf("%q: problem %q", text, problem)
		}
	}
}

func TestStructuredOutputAcceptsStringEncodedJSONAndKeepsStringSchemasAsStrings(t *testing.T) {
	horizonSchema, err := compileSchema([]byte(`{
		"type": "object",
		"required": ["status", "reason", "body_path", "tests"],
		"properties": {
			"status": {"type": "string", "enum": ["ok", "fail"]},
			"reason": {"type": "string"},
			"body_path": {"type": "string"},
			"tests": {"type": "array", "items": {"type": "string"}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	tool := structuredOutput(horizonSchema)
	// Claude often sends an untyped object parameter as a JSON-encoded string.
	args := []byte(`{"result":"{\"status\":\"ok\",\"reason\":\"done\",\"body_path\":\"out.md\",\"tests\":[\"a\"]}"}`)
	res, err := tool.Execute(t.Context(), "call-1", args, nil)
	if err != nil {
		t.Fatalf("string-encoded object rejected: %v", err)
	}
	if !strings.Contains(string(tool.Parameters), `"result":{"type":"object"}`) || strings.Contains(string(tool.Parameters), `"body_path"`) {
		t.Fatalf("object parameters = %s", tool.Parameters)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Details.(json.RawMessage), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" || got["body_path"] != "out.md" {
		t.Fatalf("decoded result = %v", got)
	}

	// An invalid string-encoded value is still rejected.
	if _, err := tool.Execute(t.Context(), "call-2", []byte(`{"result":"{\"status\":\"bogus\"}"}`), nil); err == nil {
		t.Fatal("invalid string-encoded object was accepted")
	}
	if _, err := tool.Execute(t.Context(), "call-3", []byte(`{"result":"not json"}`), nil); err == nil {
		t.Fatal("plain string for object schema was accepted")
	}

	// A schema of type string still receives the plain string directly, even if its text looks like JSON.
	stringSchema, err := compileSchema([]byte(`{"type":"string"}`))
	if err != nil {
		t.Fatal(err)
	}
	stringTool := structuredOutput(stringSchema)
	if !strings.Contains(string(stringTool.Parameters), `"result":{"type":"string"}`) {
		t.Fatalf("string parameters = %s", stringTool.Parameters)
	}
	for _, raw := range []string{`"plain text"`, `"{\"status\":\"ok\"}"`} {
		res, err := stringTool.Execute(t.Context(), "call-str", []byte(`{"result":`+raw+`}`), nil)
		if err != nil {
			t.Fatalf("string %s rejected: %v", raw, err)
		}
		if string(res.Details.(json.RawMessage)) != raw {
			t.Fatalf("string %s decoded as %s", raw, res.Details.(json.RawMessage))
		}
	}

	// A failing string schema reports the string constraint error, not a decoded-number type mismatch.
	minLenSchema, err := compileSchema([]byte(`{"type":"string","minLength":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := structuredOutput(minLenSchema).Execute(t.Context(), "call-min", []byte(`{"result":"123"}`), nil); err == nil || !strings.Contains(err.Error(), "minLength") {
		t.Fatalf("minLength error = %v", err)
	}
}

func TestStructuredOutputParameterTypeHints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema string
		want   string
	}{
		{"object", `{"type":"object","properties":{"a":{"type":"string"}}}`, `"result":{"type":"object"}`},
		{"array", `{"type":"array","items":{"type":"string"}}`, `"result":{"type":"array","items":{}}`},
		{"string", `{"type":"string"}`, `"result":{"type":"string"}`},
		{"number", `{"type":"number"}`, `"result":{"type":"number"}`},
		{"integer", `{"type":"integer"}`, `"result":{"type":"integer"}`},
		{"boolean", `{"type":"boolean"}`, `"result":{"type":"boolean"}`},
		{"null", `{"type":"null"}`, `"result":{}`},
		{"union", `{"type":["object","null"]}`, `"result":{}`},
		{"untyped", `{"anyOf":[{"type":"object"},{"type":"string"}]}`, `"result":{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := compileSchema([]byte(tc.schema))
			if err != nil {
				t.Fatal(err)
			}
			params := string(structuredOutput(s).Parameters)
			if !strings.Contains(params, tc.want) {
				t.Errorf("parameters = %s, want substring %s", params, tc.want)
			}
		})
	}
}

func TestReadResultExtractsLastFencedBlockOrJSONAfterProse(t *testing.T) {
	objSchema, err := compileSchema([]byte(`{"type":"object","required":["count"],"properties":{"count":{"type":"number"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		text string
		want float64
	}{
		{"prose before object", "The changes are complete.\n\n{\"count\": 3}", 3},
		{"prose before and after object", "Here is the result:\n{\"count\": 4}\nAll tests pass.", 4},
		{"prose before fenced block", "The changes are complete.\n```json\n{\"count\": 5}\n```", 5},
		{"prose around fenced block", "Summary:\n```json\n{\"count\": 6}\n```\nDone.", 6},
		{"two fenced blocks", "```go\nfunc f() {}\n```\n```json\n{\"count\": 7}\n```", 7},
		{"two json fenced blocks takes last", "```\n{\"count\": 1}\n```\n```\n{\"count\": 2}\n```", 2},
		{"fenced shell then bare json", "```sh\ngo test ./...\n```\n{\"count\": 8}", 8},
		{"fenced json then fenced shell", "```json\n{\"count\": 9}\n```\n```sh\ngo test ./...\n```", 9},
		{"backticks inside json string in fence", "```json\n{\"count\": 10, \"note\": \"```go\\nx\\n```\"}\n```", 10},
		{"braces and escaped quotes in string", `Done: {"count": 11, "note": "a } \"b\" \\"}`, 11},
		{"unmatched quote before json", `Fixed 24" monitor bug: {"count": 12}`, 12},
		{"unmatched quote after json", `Result: {"count": 13} on 24" monitor.`, 13},
		{"trailing non-json braces and markdown link", "Result: {\"count\": 14}\nSee [docs](https://example.com) and {placeholder}.", 14},
		{"crlf fenced block", "Done.\r\n```json\r\n{\"count\": 15}\r\n```\r\n", 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, problem := readResult(objSchema, tc.text)
			if problem != "" {
				t.Fatalf("unexpected problem: %s", problem)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got["count"] != tc.want {
				t.Fatalf("count = %v, want %v", got["count"], tc.want)
			}
		})
	}

	arrSchema, err := compileSchema([]byte(`{"type":"array","items":{"type":"integer"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw, problem := readResult(arrSchema, "Found these IDs:\n[1, 2, 3]"); problem != "" || string(raw) != "[1,2,3]" {
		t.Fatalf("array after prose: raw=%s problem=%q", raw, problem)
	}

	// Refusal cases: invalid JSON, wrong shape, nested object inside invalid top-level object.
	for _, tc := range []struct {
		name        string
		text        string
		wantContain string
	}{
		{"plain prose", "Done.", "not JSON"},
		{"invalid json after prose", "Here is the result: {count: 3}", "not JSON"},
		{"invalid json in fence", "```json\n{bad}\n```", "not JSON"},
		{"wrong shape after prose", `Here is the result: {"count": "many"}`, "want number"},
		{"wrong shape in fence", "```json\n{\"count\": \"many\"}\n```", "want number"},
		{"nested object not plucked from top-level object", `Result: {"outer": {"count": 1}}`, "missing property"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, problem := readResult(objSchema, tc.text)
			if raw != nil || problem == "" || !strings.Contains(problem, tc.wantContain) {
				t.Fatalf("raw=%s problem=%q, want substring %q", raw, problem, tc.wantContain)
			}
		})
	}
}
