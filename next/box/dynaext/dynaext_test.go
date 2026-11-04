package dynaext

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"

	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// session is a session with the extension, whose dyna server records each post the engine would receive.
type session struct {
	harness *pig.Harness
	model   *pigtest.Model
	dir     string
	mu      sync.Mutex
	posts   []json.RawMessage
}

func newSession(t *testing.T, script ...[]pigtest.Block) *session {
	t.Helper()
	return newSessionOn(t, "test-model", script...)
}

// newSessionOn is newSession on the named model of pigtest.AgentDir.
func newSessionOn(t *testing.T, model string, script ...[]pigtest.Block) *session {
	t.Helper()
	b := &session{model: pigtest.NewModel(script...), dir: t.TempDir()}
	dyna, err := dynamcp.New(func(_ context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
		if method != contract.MethodPost {
			return nil, &contract.Error{Code: contract.ErrInvalid, Message: "unexpected " + string(method)}
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.posts = append(b.posts, body)
		return []byte(`{"event":"ev_1"}`), nil
	}, nil, post.Preparer{Dir: b.dir, Upload: func(context.Context, string) (contract.Attachment, error) { return contract.Attachment{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	server := shell.New(shell.Config{After: time.After})
	// The dyna server is listed as a box lists it: deferred, with post direct.
	mcp := httptest.NewServer(dyna)
	t.Cleanup(mcp.Close)
	agentDir := pigtest.AgentDir(t, b.model, `{"dyna":{"url":"`+mcp.URL+`","exposure":"deferred","toolExposure":{"post":"direct"}}}`)
	b.harness, err = pig.Open(context.Background(), pig.Config{
		Dir:        b.dir,
		AgentDir:   agentDir,
		SessionDir: filepath.Join(agentDir, "sessions"),
		Model:      contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: model, Thinking: contract.ThinkingLow},
		Extensions: []extension.InlineExtension{Extension(Config{Dir: b.dir, Shell: server, Dyna: dyna})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.harness.Close() })
	return b
}

// turn runs a turn with the texts as its inputs, in order.
func (b *session) turn(t *testing.T, texts ...string) contract.TurnEnded {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inputs := make([]contract.TurnInput, len(texts))
	for i, text := range texts {
		inputs[i] = contract.TurnInput{Delivery: contract.DeliverySeq(i + 1), Text: text}
	}
	result, err := b.harness.Start(ctx, "turn_a", inputs, func() {})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// sent is the nth post the engine received.
func (b *session) sent(t *testing.T, n int) contract.PostRequest {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if n >= len(b.posts) {
		t.Fatalf("%d posts, not %d", len(b.posts), n+1)
	}
	var request contract.PostRequest
	if err := json.Unmarshal(b.posts[n], &request); err != nil {
		t.Fatal(err)
	}
	return request
}

// toolResult is the text of the tool result in the nth request's last message.
func (b *session) toolResult(t *testing.T, n int) string {
	t.Helper()
	messages := b.model.Requests()[n].Messages
	return string(messages[len(messages)-1])
}

func TestAPostWithEndTurnEndsTheTurn(t *testing.T) {
	b := newSession(t, []pigtest.Block{{Text: "Done."}, {Tool: "mcp__dyna__post", Input: `{"action":"send","text":"the reply","endTurn":true}`}})
	result := b.turn(t, "reply with a post")
	if result.Final != "" || !reflect.DeepEqual(result.EndPosts, []contract.EventID{"ev_1"}) {
		t.Fatalf("result %+v", result)
	}
	if sent := b.sent(t, 0); sent.Text != "the reply" || sent.ToolCallID != "toolu_1_1" {
		t.Fatalf("post %+v", sent)
	}
}

// A script's call never ends the turn, so its post says the turn goes on, and carries the script's own call ID.
func TestAPostFromAScriptDoesNotEndTheTurn(t *testing.T) {
	script, _ := json.Marshal(map[string]string{"code": `const r = await tools.mcp__dyna__post({action: "send", text: "progress", endTurn: true}); return JSON.stringify(r.structuredContent);`})
	b := newSession(t, []pigtest.Block{{Tool: "codemode", Input: string(script)}}, []pigtest.Block{{Text: "finished"}})
	result := b.turn(t, "post from a script")
	if result.Final != "finished" || result.EndPosts != nil {
		t.Fatalf("result %+v", result)
	}
	if sent := b.sent(t, 0); sent.ToolCallID != "toolu_1_0" {
		t.Fatalf("post %+v", sent)
	}
	if got := b.toolResult(t, 1); !strings.Contains(got, `turnContinues`) {
		t.Fatalf("tool result %s", got)
	}
}

func TestBashRunsInTheSessionTree(t *testing.T) {
	b := newSession(t, []pigtest.Block{{Tool: "bash", Input: `{"command":"pwd; exit 3"}`}}, []pigtest.Block{{Text: "ok"}})
	b.turn(t, "where am I")
	if got := b.toolResult(t, 1); !strings.Contains(got, b.dir) || !strings.Contains(got, ": exit 3]") || strings.Contains(got, `"is_error":true`) {
		t.Fatalf("tool result %s", got)
	}
}

// The model reads the end of a long output, and the whole output is in the file the note names.
func TestALongOutputIsCutToItsEnd(t *testing.T) {
	b := newSession(t, []pigtest.Block{{Tool: "bash", Input: `{"command":"seq 1 20000"}`}}, []pigtest.Block{{Text: "ok"}})
	b.turn(t, "count")
	got := b.toolResult(t, 1)
	if strings.Contains(got, `\n1\n2\n`) || !strings.Contains(got, `\n20000\n`) {
		t.Fatalf("not cut to its end: %.200s", got)
	}
	_, after, ok := strings.Cut(got, "Full output: ")
	path, _, _ := strings.Cut(after, " ")
	whole, err := os.ReadFile(path)
	if !ok || err != nil || !strings.HasPrefix(string(whole), "1\n2\n") {
		t.Fatalf("full output at %q: %v", path, err)
	}
}

func TestJobListsTheBackgroundJobs(t *testing.T) {
	b := newSession(t, []pigtest.Block{{Tool: "bash", Input: `{"command":"echo listed"}`}}, []pigtest.Block{{Tool: "job", Input: `{"action":"list"}`}}, []pigtest.Block{{Text: "ok"}})
	b.turn(t, "list")
	if got := b.toolResult(t, 2); !strings.Contains(got, `\texit 0\techo listed`) {
		t.Fatalf("tool result %s", got)
	}
}

// The first request's head names the dyna tools a search or script reaches; tool_search's description points at it,
// and no servers section is sent.
func TestTheHeadNamesTheDynaToolsASearchReaches(t *testing.T) {
	b := newSession(t, []pigtest.Block{{Text: "ok"}})
	b.turn(t, "hi")
	request := b.model.Requests()[0]
	system := string(request.System)
	if !strings.Contains(system, `<deferred_tools>\nTools not declared to you yet`) || !strings.Contains(system, `\nmcp__dyna__:`) || !strings.Contains(system, `\n- session: `) || strings.Contains(system, "<mcp_servers>") {
		t.Fatalf("system prompt: %s", system)
	}
	var search bool
	for _, tool := range request.Tools {
		search = search || strings.Contains(string(tool), "The `deferred_tools` section of the system prompt names the tools")
	}
	if !search {
		t.Fatal("tool_search's description does not name the section")
	}
}

// filler makes a turn big enough for a compaction to have something to summarize before the kept recent turn.
var filler = strings.Repeat("word ", 20000)

// toolNames is the names of the tools a request declares at its top level.
func toolNames(t *testing.T, request pigtest.Request) []string {
	t.Helper()
	names := make([]string, len(request.Tools))
	for i, tool := range request.Tools {
		var named struct{ Name string }
		if err := json.Unmarshal(tool, &named); err != nil {
			t.Fatal(err)
		}
		names[i] = named.Name
	}
	return names
}

// compact compacts the conversation, as an idle box does.
func (b *session) compact(t *testing.T) {
	t.Helper()
	if got, err := b.harness.Compact(context.Background(), 0); err != nil || !got.Compacted {
		t.Fatalf("compaction %+v, %v", got, err)
	}
}

// last is the names of the tools the latest request declared.
func (b *session) last(t *testing.T) []string {
	t.Helper()
	requests := b.model.Requests()
	return toolNames(t, requests[len(requests)-1])
}

// loadGoalAndRoom loads the goal and room tools with one search each, then calls goal in the way the test says.
func loadGoalAndRoom(call pigtest.Block) [][]pigtest.Block {
	return [][]pigtest.Block{
		{{Tool: "tool_search", Input: `{"query":"mcp__dyna__goal","limit":1}`}},
		{{Tool: "tool_search", Input: `{"query":"mcp__dyna__room","limit":1}`}},
		{call},
		{{Text: "ok"}},
		{{Text: "ok2"}},
		{{Text: "the summary"}},
		{{Text: "after"}},
	}
}

func goalCall() pigtest.Block {
	return pigtest.Block{Tool: "mcp__dyna__goal", Input: `{"action":"list"}`}
}

// A compaction keeps a loaded tool that was called, and unloads one that was not; the base tools stay.
func TestACompactionUnloadsALoadedToolNothingCalled(t *testing.T) {
	b := newSession(t, loadGoalAndRoom(goalCall())...)
	b.turn(t, "hi "+filler)
	b.turn(t, "more "+filler)
	b.compact(t)
	b.turn(t, "again")
	got := b.last(t)
	for _, want := range []string{"read", "bash", "edit", "write", "codemode", "job", "mcp__dyna__post", "tool_search", "mcp__dyna__goal"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is not declared after the compaction: %v", want, got)
		}
	}
	if slices.Contains(got, "mcp__dyna__room") {
		t.Errorf("room, loaded and never called, is still declared: %v", got)
	}
}

// A call from a script counts as a use.
func TestAToolCalledFromAScriptSurvivesACompaction(t *testing.T) {
	script, _ := json.Marshal(map[string]string{"code": `await tools.mcp__dyna__goal({action: "list"}); return "done";`})
	b := newSession(t, loadGoalAndRoom(pigtest.Block{Tool: "codemode", Input: string(script)})...)
	b.turn(t, "hi "+filler)
	b.turn(t, "more "+filler)
	b.compact(t)
	b.turn(t, "again")
	if got := b.last(t); !slices.Contains(got, "mcp__dyna__goal") || slices.Contains(got, "mcp__dyna__room") {
		t.Fatalf("declared %v", got)
	}
}

// Nothing is unloaded until a compaction.
func TestNoToolIsUnloadedWithoutACompaction(t *testing.T) {
	b := newSession(t, loadGoalAndRoom(goalCall())...)
	b.turn(t, "hi "+filler)
	b.turn(t, "more "+filler)
	if got := b.last(t); !slices.Contains(got, "mcp__dyna__room") || !slices.Contains(got, "mcp__dyna__goal") {
		t.Fatalf("declared %v", got)
	}
}

// What counts as used is what was called since the previous compaction: a tool used before the first one is unloaded at
// the second when the window between them did not call it.
func TestTheUsedToolsResetAfterEachCompaction(t *testing.T) {
	script := loadGoalAndRoom(goalCall())
	script = append(script[:6], []pigtest.Block{{Text: "ok3"}}, []pigtest.Block{{Text: "ok4"}}, []pigtest.Block{{Text: "the second summary"}}, []pigtest.Block{{Text: "after"}})
	b := newSession(t, script...)
	b.turn(t, "hi "+filler)
	b.turn(t, "more "+filler)
	b.compact(t)
	b.turn(t, "quiet")
	b.turn(t, "again "+filler)
	if got := b.last(t); !slices.Contains(got, "mcp__dyna__goal") {
		t.Fatalf("goal, used before the first compaction, is unloaded before the second: %v", got)
	}
	b.compact(t)
	b.turn(t, "last")
	if got := b.last(t); slices.Contains(got, "mcp__dyna__goal") {
		t.Fatalf("goal, not called since the first compaction, is still declared: %v", got)
	}
}

// A search without a limit loads three tools.
func TestASearchLoadsThreeToolsUnlessTheModelSaysMore(t *testing.T) {
	b := newSession(t, []pigtest.Block{{Tool: "tool_search", Input: `{"query":"mcp__dyna"}`}}, []pigtest.Block{{Text: "ok"}})
	b.turn(t, "hi")
	if got := b.toolResult(t, 1); !strings.Contains(got, "Loaded 3 tools") {
		t.Fatalf("tool result %s", got)
	}
}

// On a route that takes tool changes mid-conversation, the head after a compaction declares what the session has, so
// the unloaded tool's definition is gone from the request and no removal note stands in its place (dyna patch P11).
func TestTheHeadAfterACompactionDeclaresOnlyTheToolsLeft(t *testing.T) {
	b := newSessionOn(t, "tools-model", loadGoalAndRoom(goalCall())...)
	b.turn(t, "hi "+filler)
	b.turn(t, "more "+filler)
	b.compact(t)
	b.turn(t, "again")
	requests := b.model.Requests()
	last := requests[len(requests)-1]
	if strings.Contains(string(last.System), "mcp__dyna__room") && !strings.Contains(string(last.System), "deferred_tools") {
		t.Fatal("room is named outside the deferred tools")
	}
	if slices.Contains(toolNames(t, last), "mcp__dyna__room") {
		t.Fatalf("room is declared at the top level: %v", toolNames(t, last))
	}
	for _, message := range last.Messages {
		if strings.Contains(string(message), "tool_removal") || strings.Contains(string(message), "mcp__dyna__room") {
			t.Fatalf("the request still carries room: %.300s", message)
		}
	}
}
