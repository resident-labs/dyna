package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

type agentRecorder struct {
	mu      sync.Mutex
	started []contract.AgentStartedRequest
	ended   chan contract.AgentEndedRequest
}

func newAgentRecorder() *agentRecorder {
	return &agentRecorder{ended: make(chan contract.AgentEndedRequest, 8)}
}

func (r *agentRecorder) AgentStarted(_ context.Context, req contract.AgentStartedRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, req)
	return nil
}

func (r *agentRecorder) AgentEnded(_ context.Context, req contract.AgentEndedRequest) error {
	r.ended <- req
	return nil
}

func (r *agentRecorder) wait(t *testing.T) contract.AgentEndedRequest {
	t.Helper()
	select {
	case ended := <-r.ended:
		return ended
	case <-time.After(30 * time.Second):
		t.Fatal("the agent never ended")
		return contract.AgentEndedRequest{}
	}
}

func requestTools(t *testing.T, r pigtest.Request) []string {
	t.Helper()
	var names []string
	for _, raw := range r.Tools {
		var tool struct{ Name string }
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// A read-only agent on a real PiG session reads a file only its parent's box has, is offered exactly the read-only tools on every
// turn, resumes its conversation on the second, and cannot write: a write call has no tool to reach.
func TestAnInBoxAgentReadsTheParentsFilesAndOnlyReads(t *testing.T) {
	work, agentDir := t.TempDir(), ""
	secret := filepath.Join(work, "uncommitted.txt")
	if err := os.WriteFile(secret, []byte("only the parent's box has this"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := pigtest.NewModel(
		[]pigtest.Block{{Tool: "read", Input: `{"path":"` + secret + `"}`}}, []pigtest.Block{{Text: "it says: only the parent's box has this"}},
		[]pigtest.Block{{Tool: "write", Input: `{"path":"` + filepath.Join(work, "new.txt") + `","content":"x"}`}}, []pigtest.Block{{Text: "could not write"}},
	)
	agentDir = pigtest.AgentDir(t, model, `{"fixture":{"command":"true","exposure":"direct"}}`)
	pigtest.Packages(t, agentDir, pigtest.Package(t, pigtest.ToolExtension("package_tool", "x")))
	var models ModelsConfig // the test model's, which AgentDir wrote
	data, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil || json.Unmarshal(data, &models) != nil {
		t.Fatal("no models", err)
	}
	sessions := filepath.Join(agentDir, "sessions")
	engine := newAgentRecorder()
	agents := NewAgents(context.Background(), AgentsConfig{
		Engine: engine, Transcript: NewTranscript(&transcriptEngine{}, sessions), Sessions: sessions,
		Open: func(ctx context.Context, turn contract.AgentTurn, dir string, toolCall func()) (agentSession, error) {
			return openAgent(ctx, agentDir, work, "core", models, nil, turn, dir, toolCall)
		},
	})
	turn := contract.AgentTurn{ID: "call_1", Name: "advisor", Tools: contract.AgentReadOnlyTools, Persist: true, Instructions: "You advise.", Prompt: "what is in uncommitted.txt?", Turn: "turn_a", Delivery: 1,
		Profile: contract.WorkflowProfile{Name: "advisor", Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}}}
	agents.Turn(turn)
	first := engine.wait(t)
	if first.Outcome != contract.TurnCompleted || !strings.Contains(first.Final, "only the parent's box has this") || !first.Fresh || first.ID != "call_1" {
		t.Fatalf("first answer %+v", first)
	}
	turn.ID, turn.Prompt, turn.Delivery = "call_2", "write a file", 2
	agents.Turn(turn)
	second := engine.wait(t)
	if second.Fresh || second.Final != "could not write" {
		t.Fatalf("second answer %+v", second)
	}
	want := slices.Clone(contract.AgentReadOnlyTools)
	slices.Sort(want)
	for i, request := range model.Requests() {
		if got := requestTools(t, request); !slices.Equal(got, want) {
			t.Fatalf("request %d offered %v, want %v", i, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "new.txt")); err == nil {
		t.Fatal("the agent wrote a file")
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.started) != 2 || !strings.HasPrefix(engine.started[0].Transcript, "agents/advisor/") || engine.started[0].Offset != 0 || engine.started[1].Transcript != engine.started[0].Transcript || engine.started[1].Offset == 0 {
		t.Fatalf("started %+v", engine.started)
	}
	if files := agents.Files(); len(files) != 1 {
		t.Fatalf("files %v", files)
	}
}

// fakeAgent is a session that makes tool calls until told to stop, or to answer.
type fakeAgent struct {
	file      string
	toolCall  func()
	before    func(context.Context) error
	obey      bool // answers when told to
	mu        sync.Mutex
	steers    []string
	aborted   chan struct{}
	hold      chan struct{} // when set, the turn waits for it, or for an abort
	abortOnce sync.Once
}

func (f *fakeAgent) Start(ctx context.Context, _ contract.TurnID, _ []contract.TurnInput, _ func()) (contract.TurnEnded, error) {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-f.aborted:
			return contract.TurnEnded{Final: "partial text", Outcome: contract.TurnStopped}, nil
		}
		return contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "held"}, nil
	}
	for {
		f.toolCall()
		if err := f.before(ctx); err != nil {
			return contract.TurnEnded{Outcome: contract.TurnFailed, Error: err.Error(), Final: "partial text"}, nil
		}
		f.mu.Lock()
		told := len(f.steers) > 0
		f.mu.Unlock()
		if told && f.obey {
			return contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "answer"}, nil
		}
	}
}
func (f *fakeAgent) Steer(_ context.Context, input contract.TurnInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steers = append(f.steers, input.Text)
	return nil
}
func (f *fakeAgent) Abort(context.Context) error {
	f.abortOnce.Do(func() { close(f.aborted) })
	return nil
}
func (f *fakeAgent) SessionFile() string                             { return f.file }
func (f *fakeAgent) SetBeforeRequest(fn func(context.Context) error) { f.before = fn }
func (f *fakeAgent) Close() error                                    { return nil }

func fakeAgents(t *testing.T, make func(*fakeAgent)) (*Agents, *agentRecorder) {
	sessions := t.TempDir()
	engine := newAgentRecorder()
	return NewAgents(context.Background(), AgentsConfig{
		Engine: engine, Transcript: NewTranscript(&transcriptEngine{}, sessions), Sessions: sessions,
		Open: func(_ context.Context, _ contract.AgentTurn, dir string, toolCall func()) (agentSession, error) {
			f := &fakeAgent{file: filepath.Join(dir, "a.jsonl"), toolCall: toolCall, aborted: make2()}
			make(f)
			return f, nil
		},
	}), engine
}

func make2() chan struct{} { return make(chan struct{}) }

// An agent told to answer at 40 tool calls gets one more request, and its answer stands. One that keeps calling tools is ended
// with the text it had.
func TestAnAgentIsToldToAnswerAtTheToolCallCap(t *testing.T) {
	for _, obey := range []bool{true, false} {
		var fake *fakeAgent
		agents, engine := fakeAgents(t, func(f *fakeAgent) { f.obey, fake = obey, f })
		agents.Turn(contract.AgentTurn{ID: "c", Name: "advisor", Tools: contract.AgentReadOnlyTools})
		ended := engine.wait(t)
		fake.mu.Lock()
		steers := slices.Clone(fake.steers)
		fake.mu.Unlock()
		if len(steers) != 1 || !strings.Contains(steers[0], "answer now") {
			t.Fatalf("obey=%v steers %q", obey, steers)
		}
		if obey && (ended.Outcome != contract.TurnCompleted || ended.Final != "answer") {
			t.Fatalf("an agent that answered: %+v", ended)
		}
		if !obey && (ended.Outcome != contract.TurnStopped || ended.Final != "partial text" || !strings.Contains(ended.Error, "told to answer")) {
			t.Fatalf("an agent that went on: %+v", ended)
		}
	}
}

// Stop ends the turn and the answer carries the text so far. A second turn of the same name waits for the first.
func TestStopEndsAnAgentAndTheSameNameWaits(t *testing.T) {
	hold := make(chan struct{})
	agents, engine := fakeAgents(t, func(f *fakeAgent) { f.hold = hold })
	agents.Turn(contract.AgentTurn{ID: "one", Name: "advisor"})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		engine.mu.Lock()
		running := len(engine.started)
		engine.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first agent never started")
		}
	}
	agents.Turn(contract.AgentTurn{ID: "two", Name: "advisor"})
	select {
	case ended := <-engine.ended:
		t.Fatalf("an agent ended before it was stopped or released: %+v", ended)
	case <-time.After(200 * time.Millisecond):
	}
	agents.Stop("one")
	if ended := engine.wait(t); ended.ID != "one" || ended.Outcome != contract.TurnStopped || ended.Final != "partial text" {
		t.Fatalf("stopped agent %+v", ended)
	}
	close(hold)
	if ended := engine.wait(t); ended.ID != "two" || ended.Outcome != contract.TurnCompleted {
		t.Fatalf("the waiting agent %+v", ended)
	}
	agents.Stop("two") // an ended agent is gone: no panic, nothing to stop
}

// A turn that passes its deadline is ended by the box itself, with the text it had, whether or not the engine is there to stop it.
func TestAnAgentTurnEndsAtItsDeadline(t *testing.T) {
	agents, engine := fakeAgents(t, func(f *fakeAgent) { f.hold = make(chan struct{}) })
	agents.Turn(contract.AgentTurn{ID: "late", Name: "advisor", TimeoutMS: 100})
	ended := engine.wait(t)
	if ended.Outcome != contract.TurnStopped || ended.Final != "partial text" || !strings.Contains(ended.Error, "ran out of time") {
		t.Fatalf("ended %+v", ended)
	}
}

// A stop while the model is streaming keeps what it had streamed: the turn settles after the abort, and the text is taken then.
func TestStoppingAStreamingAgentKeepsItsPartialAnswer(t *testing.T) {
	streaming := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial answer\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case streaming <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	agentDir := pigtest.AgentDir(t, pigtest.NewModel(), "")
	var models ModelsConfig
	data, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil || json.Unmarshal(data, &models) != nil {
		t.Fatal("no models", err)
	}
	provider := models.Providers["anthropic"]
	provider.BaseURL = server.URL
	models.Providers["anthropic"] = provider
	sessions := filepath.Join(agentDir, "sessions")
	engine := newAgentRecorder()
	agents := NewAgents(context.Background(), AgentsConfig{
		Engine: engine, Transcript: NewTranscript(&transcriptEngine{}, sessions), Sessions: sessions,
		Open: func(ctx context.Context, turn contract.AgentTurn, dir string, toolCall func()) (agentSession, error) {
			return openAgent(ctx, agentDir, t.TempDir(), "core", models, nil, turn, dir, toolCall)
		},
	})
	agents.Turn(contract.AgentTurn{ID: "call_1", Name: "advisor", Tools: contract.AgentReadOnlyTools, Prompt: "hi", Turn: "turn_a", Delivery: 1,
		Profile: contract.WorkflowProfile{Name: "advisor", Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}}})
	select {
	case <-streaming:
	case <-time.After(10 * time.Second):
		t.Fatal("the model never streamed")
	}
	time.Sleep(200 * time.Millisecond) // the stream's first text has reached the session
	agents.Stop("call_1")
	if ended := engine.wait(t); ended.Outcome != contract.TurnStopped || ended.Final != "partial answer" {
		t.Fatalf("ended %+v", ended)
	}
}

// blockedAbort is a session whose Abort waits for it to be idle, which it never is until released.
type blockedAbort struct {
	*fakeAgent
	entered, release chan struct{}
	once             sync.Once
}

func (s *blockedAbort) Abort(ctx context.Context) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.fakeAgent.Abort(ctx)
}

// A stop whose Abort never returns still ends the turn within the settle bound, and frees the name for the next turn.
func TestAStopWhoseAbortBlocksStillEndsTheTurnAndFreesTheName(t *testing.T) {
	was := agentSettleFor(100 * time.Millisecond)
	defer agentSettleFor(was)
	sessions := t.TempDir()
	blocked := &blockedAbort{fakeAgent: &fakeAgent{file: filepath.Join(sessions, "a.jsonl"), aborted: make(chan struct{}), hold: make(chan struct{})}, entered: make(chan struct{}), release: make(chan struct{})}
	defer close(blocked.release)
	engine := newAgentRecorder()
	opened := 0
	agents := NewAgents(context.Background(), AgentsConfig{
		Engine: engine, Transcript: NewTranscript(&transcriptEngine{}, sessions), Sessions: sessions,
		Open: func(context.Context, contract.AgentTurn, string, func()) (agentSession, error) {
			opened++
			if opened == 1 {
				return blocked, nil
			}
			return &fakeAgent{file: filepath.Join(sessions, "b.jsonl"), aborted: make(chan struct{}), hold: func() chan struct{} { c := make(chan struct{}); close(c); return c }()}, nil
		},
	})
	agents.Turn(contract.AgentTurn{ID: "stuck", Name: "advisor"})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		engine.mu.Lock()
		running := len(engine.started)
		engine.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent never started")
		}
	}
	agents.Stop("stuck")
	<-blocked.entered
	if ended := engine.wait(t); ended.ID != "stuck" || ended.Outcome != contract.TurnStopped {
		t.Fatalf("the stuck agent ended %+v", ended)
	}
	agents.Turn(contract.AgentTurn{ID: "next", Name: "advisor"})
	if ended := engine.wait(t); ended.ID != "next" || ended.Outcome != contract.TurnCompleted {
		t.Fatalf("the next agent ended %+v", ended)
	}
}
