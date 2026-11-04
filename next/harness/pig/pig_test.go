package pig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// config is a session on the model, with the given MCP servers.
func config(t *testing.T, model *pigtest.Model, mcpServers string) Config {
	dir := pigtest.AgentDir(t, model, mcpServers)
	return Config{
		Dir:          t.TempDir(),
		AgentDir:     dir,
		SessionDir:   filepath.Join(dir, "sessions"),
		Model:        contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow},
		Instructions: "You are ses_test.",
	}
}

func open(t *testing.T, model *pigtest.Model, extensions ...extension.InlineExtension) *Harness {
	t.Helper()
	c := config(t, model, "")
	c.Extensions = extensions
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func start(t *testing.T, h *Harness, texts ...string) contract.TurnEnded {
	t.Helper()
	inputs := make([]contract.TurnInput, len(texts))
	for i, text := range texts {
		inputs[i] = contract.TurnInput{Delivery: contract.DeliverySeq(i + 1), Text: text}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := false
	result, err := h.Start(ctx, "turn_a", inputs, func() { started = true })
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("the turn never reported that it started")
	}
	return result
}

// testTool is a tool an extension registers, with a fixed result.
func testTool(name string, result extension.AgentToolResult) extension.InlineExtension {
	return extension.NamedInlineExtension{Name: "test-" + name, Factory: func(api extension.API) error {
		api.RegisterTool(extension.ToolDefinition{
			Name: name, Label: name, Description: "A test tool.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
			Execute: func(context.Context, string, json.RawMessage, extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
				return result, nil
			},
		})
		return nil
	}}
}

func TestATurnAnswersAndRecordsTheSession(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "hello"}})
	h := open(t, model)
	result := start(t, h, "say hello")
	if result.Outcome != contract.TurnCompleted || result.Final != "hello" || result.Usage.Model != "test-model" || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Fatalf("result %+v", result)
	}
	if _, err := os.Stat(h.SessionFile()); err != nil {
		t.Fatalf("session file: %v", err)
	}
}

// The appended instructions reach the model, and nothing points it at documentation that is not in the box.
func TestTheSystemPromptCarriesTheInstructionsAndNoDocs(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "ok"}})
	start(t, open(t, model), "hi")
	system := string(model.Requests()[0].System)
	if !strings.Contains(system, "You are ses_test.") {
		t.Fatalf("instructions missing from %s", system)
	}
	for _, docs := range []string{"docs/extensions.md", "documentation"} {
		if strings.Contains(system, docs) {
			t.Fatalf("the prompt names documentation (%q): %s", docs, system)
		}
	}
}

// Inputs that arrive together are each a user message of the first request.
func TestEachInputOfABatchIsItsOwnMessageInTheFirstRequest(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "both"}})
	result := start(t, open(t, model), "first", "second")
	messages := model.Requests()[0].Messages
	if len(messages) != 2 || !strings.Contains(string(messages[0]), "first") || !strings.Contains(string(messages[1]), "second") {
		t.Fatalf("messages %s", messages)
	}
	if result.Final != "both" {
		t.Fatalf("result %+v", result)
	}
}

// A tool whose result terminates the batch ends the turn with its post as the reply, and no further request is made.
func TestAPostThatEndsTheTurn(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "Posting."}, {Tool: "post_test", Input: `{}`}})
	h := open(t, model, testTool("post_test", extension.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "posted"}}, Details: EndPost{EndPost: "ev_1"}, Terminate: true}))
	result := start(t, h, "post it")
	if result.Final != "" || !reflect.DeepEqual(result.EndPosts, []contract.EventID{"ev_1"}) {
		t.Fatalf("result %+v", result)
	}
	if n := len(model.Requests()); n != 1 {
		t.Fatalf("%d requests after the turn ended", n)
	}
}

// A script makes many nested calls, and each publishes an event. The session must not wait for a reader of its event
// channel, which nothing reads.
func TestALongScriptDoesNotStallTheSession(t *testing.T) {
	var calls atomic.Int32
	counter := extension.NamedInlineExtension{Name: "test-count", Factory: func(api extension.API) error {
		api.RegisterTool(extension.ToolDefinition{
			Name: "count", Label: "count", Description: "Counts.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
			Execute: func(context.Context, string, json.RawMessage, extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
				calls.Add(1)
				return extension.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "1"}}}, nil
			},
		})
		return nil
	}}
	script, _ := json.Marshal(map[string]string{"code": "for (let i = 0; i < 200; i++) await tools.count({}); return 'done';"})
	model := pigtest.NewModel([]pigtest.Block{{Tool: "codemode", Input: string(script)}}, []pigtest.Block{{Text: "finished"}})
	result := start(t, open(t, model, counter), "count")
	if result.Final != "finished" || calls.Load() != 200 {
		t.Fatalf("result %+v after %d calls", result, calls.Load())
	}
}

func TestAServerThatDoesNotConnectIsANotice(t *testing.T) {
	notices := make(chan string, 4)
	c := config(t, pigtest.NewModel(), `{"gone":{"url":"http://127.0.0.1:1/mcp","exposure":"direct"}}`)
	c.Notice = func(text string) { notices <- text }
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	select {
	case text := <-notices:
		if !strings.Contains(text, "gone") || strings.Contains(text, "/mcp to fix") {
			t.Fatalf("notice %q", text)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no notice")
	}
}

// A new box continues the conversation its session folder holds.
func TestOpenContinuesTheNewestSession(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "one"}}, []pigtest.Block{{Text: "two"}})
	c := config(t, model, "")
	first, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	start(t, first, "remember one")
	path := first.SessionFile()
	_ = first.Close()
	second, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.SessionFile() != path {
		t.Fatalf("opened %s, not %s", second.SessionFile(), path)
	}
	start(t, second, "and two")
	if messages := model.Requests()[1].Messages; len(messages) != 3 {
		t.Fatalf("the second turn sent %d messages: %s", len(messages), messages)
	}
}

func TestTransferToTheSameModelIsUnchanged(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "hello"}})
	h := open(t, model)
	start(t, h, "hi")
	var kinds []contract.ActivityKind
	h.Activity = func(_ contract.TurnID, kind contract.ActivityKind, _ string) { kinds = append(kinds, kind) }
	result, err := h.TransferModel(context.Background(), "turn_b", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingHigh}, 50_000)
	if err != nil || result.Path != "unchanged" || result.TokensBefore == 0 || !reflect.DeepEqual(kinds, []contract.ActivityKind{contract.ActivityModel}) {
		t.Fatalf("%+v %v %v", result, err, kinds)
	}
	if level := h.session.ThinkingLevel(); level != "high" {
		t.Fatalf("thinking %s", level)
	}
}

// A provider the box does not have is refused, so the turn fails without a model change.
func TestTransferToAnUnknownModelIsRefused(t *testing.T) {
	h := open(t, pigtest.NewModel())
	_, err := h.TransferModel(context.Background(), "turn_b", contract.ModelChoice{Provider: "no-such-provider", ID: "m", Thinking: contract.ThinkingLow}, 0)
	if !errors.Is(err, harness.ErrRefused) || h.session.Model().ID != "test-model" {
		t.Fatalf("err %v on %s", err, h.session.Model().ID)
	}
}

// longTurns are three turns that leave 16,000 tokens of context, more than small-model can take, the first two of about
// 7,500 tokens each, which a summary can replace. The model answers each with "read".
func longTurns(t *testing.T, model *pigtest.Model, h *Harness) {
	t.Helper()
	start(t, h, strings.Repeat("word ", 6_000))
	start(t, h, strings.Repeat("more ", 6_000))
	model.InputTokens = 16_000
	start(t, h, "read this")
	model.InputTokens = 0
}

// A conversation too big for the new model is summarized first, and the session moves.
func TestTransferToASmallerModelCompacts(t *testing.T) {
	read := []pigtest.Block{{Text: "read"}}
	model := pigtest.NewModel(read, read, read, []pigtest.Block{{Text: "the summary"}})
	h := open(t, model)
	longTurns(t, model, h)
	result, err := h.TransferModel(context.Background(), "turn_b", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "small-model", Thinking: contract.ThinkingLow}, 0)
	if err != nil || result.Path != "compacted" || result.TokensAfter >= result.TokensBefore || h.session.Model().ID != "small-model" {
		t.Fatalf("%+v %v on %s", result, err, h.session.Model().ID)
	}
}

// The compaction trigger is the session's from the transfer on: a conversation over it is summarized.
func TestTransferAppliesTheCompactionTrigger(t *testing.T) {
	read := []pigtest.Block{{Text: "read"}}
	model := pigtest.NewModel(read, read, read, []pigtest.Block{{Text: "the summary"}})
	h := open(t, model)
	longTurns(t, model, h)
	result, err := h.TransferModel(context.Background(), "turn_b", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}, 2_000)
	if err != nil || result.Path != "compacted" {
		t.Fatalf("%+v %v", result, err)
	}
}

// A stopped transfer leaves the model as it was.
func TestAStoppedTransferKeepsTheModel(t *testing.T) {
	read := []pigtest.Block{{Text: "read"}}
	model := pigtest.NewModel(read, read, read, []pigtest.Block{{Text: "the summary"}})
	h := open(t, model)
	longTurns(t, model, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := h.TransferModel(ctx, "turn_b", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "small-model", Thinking: contract.ThinkingLow}, 0)
	if err != nil || result.Path != "aborted" || h.session.Model().ID != "test-model" {
		t.Fatalf("%+v %v on %s", result, err, h.session.Model().ID)
	}
}

// The first request holds the sections in its system prompt; a change reaches the next turn as a context update and
// leaves the system prompt as it was, so the cached prefix holds.
func TestPromptSectionsChangeThroughAContextUpdate(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "one"}}, []pigtest.Block{{Text: "two"}})
	h := open(t, model)
	set := func(text string) {
		t.Helper()
		if err := h.SetPromptSections(context.Background(), []contract.ContextSection{{Key: contract.ContextGoal, Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	set("g1: first")
	start(t, h, "hi")
	set("g1: second")
	start(t, h, "again")
	requests := model.Requests()
	if len(requests) != 2 || !strings.Contains(string(requests[0].System), `<dyna_goal>\ng1: first\n</dyna_goal>`) || string(requests[1].System) != string(requests[0].System) {
		t.Fatalf("system prompts:\n%s\n%s", requests[0].System, requests[len(requests)-1].System)
	}
	var messages []string
	for _, message := range requests[1].Messages {
		messages = append(messages, string(message))
	}
	last := strings.Join(messages, "\n")
	if !strings.Contains(last, `[Context update]\nChanged section dyna_goal:`) || !strings.Contains(last, `-g1: first\n+g1: second`) {
		t.Fatalf("the messages: %s", last)
	}
}

// A worker opens beside the files of the sessions in its folder, and starts one of its own.
func TestANewSessionNeverContinuesTheNewestFile(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "one"}}, []pigtest.Block{{Text: "two"}})
	c := config(t, model, "")
	first, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	start(t, first, "remember one")
	defer first.Close()
	c.New = true
	second, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.SessionFile() == first.SessionFile() {
		t.Fatalf("both sessions write %s", first.SessionFile())
	}
	start(t, second, "and two")
	if messages := model.Requests()[1].Messages; len(messages) != 1 {
		t.Fatalf("the new session sent %d messages: %s", len(messages), messages)
	}
}

// A session compacts at the trigger it opens with, as a worker does at its profile's.
func TestAConversationOverTheOpeningTriggerIsCompacted(t *testing.T) {
	read := []pigtest.Block{{Text: "read"}}
	model := pigtest.NewModel(read, read, read, []pigtest.Block{{Text: "the summary"}}, []pigtest.Block{{Text: "after"}})
	c := config(t, model, "")
	c.CompactTokens = 2_000
	// Compaction keeps the latest 20,000 tokens by default, which is all this conversation holds.
	if err := os.WriteFile(filepath.Join(c.AgentDir, "settings.json"), []byte(`{"defaultProjectTrust":"always","steeringMode":"all","compaction":{"keepRecentTokens":1000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	longTurns(t, model, h)
	start(t, h, "go on")
	if n := len(model.Requests()); n != 5 {
		t.Fatalf("%d requests: the conversation was not summarized before the next turn", n)
	}
}

// A session reads its models from the file it is given, so a worker's header reaches the route.
func TestModelsFileSetsTheHeadersOfEveryRequest(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "hello"}})
	c := config(t, model, "")
	data, err := os.ReadFile(filepath.Join(c.AgentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var models struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(data, &models); err != nil {
		t.Fatal(err)
	}
	models.Providers["anthropic"]["headers"] = map[string]string{contract.WorkerHeader: "w1"}
	if data, err = json.Marshal(models); err != nil {
		t.Fatal(err)
	}
	c.ModelsFile = filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(c.ModelsFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	start(t, h, "hi")
	if got := model.Requests()[0].Header.Get(contract.WorkerHeader); got != "w1" {
		t.Fatalf("the request carried %q", got)
	}
}
