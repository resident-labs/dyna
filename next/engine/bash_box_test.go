//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/hostexec"
)

// localHostexec runs host commands on this machine, as the engine does on the control plane.
type localHostexec struct{ hostexec.Local }

func (l localHostexec) StopHostexec(ctx context.Context, _ contract.HostName, scope string) error {
	return l.Local.StopHostexec(ctx, scope)
}

// toolResult is one tool result as the model got it.
type toolResult struct {
	Text    string
	IsError bool
}

// toolUpstream stands where the model proxy would be. The newest task in a conversation decides what the model does: each tool
// of its list, one a request, and then a text answer. Anything after that, such as a wake, gets text too.
type toolUpstream struct {
	mu     sync.Mutex
	tools  map[string][]crewTool
	bodies []string
	names  []string // the tools declared in the newest request
	hold   func()   // when set, runs before each answer, so a test can hold the model while it sends input
}

var newestTask = regexp.MustCompile(`task-[a-z0-9]+`)

func (m *toolUpstream) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Input    []json.RawMessage `json:"input"`
		Tools    []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(data, &body)
	responses := strings.HasSuffix(r.URL.Path, "/responses")
	messages, resultMark := body.Messages, `"tool_result"`
	if responses {
		messages, resultMark = body.Input, `_call_output"`
	}
	task, since := "", 0
	for i, message := range messages {
		var user struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(message, &user)
		if found := newestTask.FindAllString(string(message), -1); user.Role == "user" && !strings.Contains(string(message), resultMark) && len(found) > 0 {
			task, since = found[len(found)-1], i
		}
	}
	done := 0
	for _, message := range messages[min(since, len(messages)):] {
		done += strings.Count(string(message), resultMark)
	}
	m.mu.Lock()
	m.bodies = append(m.bodies, string(data))
	m.names = m.names[:0]
	for _, tool := range body.Tools {
		m.names = append(m.names, tool.Name)
	}
	for _, item := range body.Input {
		var declared struct {
			Type  string `json:"type"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(item, &declared)
		if declared.Type == "additional_tools" {
			for _, tool := range declared.Tools {
				m.names = append(m.names, tool.Name)
			}
		}
	}
	tools, hold := m.tools[task], m.hold
	m.mu.Unlock()
	if hold != nil {
		hold()
	}
	var call *crewTool
	if done < len(tools) {
		call = &tools[done]
	}
	id := fmt.Sprintf("%s_%d", strings.TrimPrefix(task, "task-"), done)
	w.Header().Set("Content-Type", "text/event-stream")
	if responses {
		serveResponses(w, call, id, strings.Contains(string(data), `"type":"custom"`))
		return
	}
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
	if call != nil {
		input, _ := json.Marshal(call.input)
		send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_%s","name":%q,"input":{}}}`, id, call.name))
		send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, input))
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
		send("message_stop", `{"type":"message_stop"}`)
		return
	}
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

// serveResponses answers as the OpenAI Responses API does: one tool call, or a text. A grammar tool's call is its raw input.
func serveResponses(w http.ResponseWriter, call *crewTool, id string, grammar bool) {
	send := func(kind string, fields map[string]any) {
		fields["type"] = kind
		data, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
	}
	send("response.created", map[string]any{"response": map[string]any{"id": "resp_1", "status": "in_progress"}})
	if call != nil && grammar && call.name == "codemode" {
		var script struct{ Code string }
		_ = json.Unmarshal([]byte(call.input), &script)
		item := map[string]any{"type": "custom_tool_call", "id": "ctc_" + id, "call_id": "call_" + id, "name": call.name, "input": ""}
		send("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		send("response.custom_tool_call_input.delta", map[string]any{"output_index": 0, "item_id": "ctc_" + id, "delta": script.Code})
		item["input"] = script.Code
		item["status"] = "completed"
		send("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	} else if call != nil {
		item := map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": "call_" + id, "name": call.name, "arguments": ""}
		send("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		send("response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "fc_" + id, "delta": call.input})
		item["arguments"] = call.input
		item["status"] = "completed"
		send("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	} else {
		item := map[string]any{"type": "message", "id": "msg_" + id, "role": "assistant", "status": "in_progress", "content": []any{}}
		send("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		send("response.content_part.added", map[string]any{"output_index": 0, "item_id": "msg_" + id, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}})
		send("response.output_text.delta", map[string]any{"output_index": 0, "item_id": "msg_" + id, "content_index": 0, "delta": "done"})
		item["status"] = "completed"
		item["content"] = []any{map[string]any{"type": "output_text", "text": "done"}}
		send("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	}
	send("response.completed", map[string]any{"response": map[string]any{"id": "resp_1", "status": "completed", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "input_tokens_details": map[string]any{"cached_tokens": 0}}}})
}

// results returns every tool result of the newest request, oldest first.
func (m *toolUpstream) results(t *testing.T) []toolResult {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input []struct {
			Type   string          `json:"type"`
			Output json.RawMessage `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(m.bodies[len(m.bodies)-1]), &body); err != nil {
		t.Fatal(err)
	}
	var results []toolResult
	for _, item := range body.Input {
		if strings.HasSuffix(item.Type, "_call_output") {
			results = append(results, toolResult{Text: string(item.Output)})
		}
	}
	for _, message := range body.Messages {
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			var parts []struct {
				Text string `json:"text"`
			}
			text := string(block.Content)
			if json.Unmarshal(block.Content, &parts) == nil {
				text = ""
				for _, part := range parts {
					text += part.Text
				}
			} else {
				_ = json.Unmarshal(block.Content, &text)
			}
			results = append(results, toolResult{text, block.IsError})
		}
	}
	return results
}

func (m *toolUpstream) firstBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bodies[0]
}

func (m *toolUpstream) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

func (m *toolUpstream) lastOf(results []toolResult) toolResult { return results[len(results)-1] }

func (m *toolUpstream) lastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bodies[len(m.bodies)-1]
}

func (m *toolUpstream) declared() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.names...)
}

func jsonOf(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// toolBox is one session in a real box on real Podman, with the real box agent and the Pi the image ships, before it starts.
type toolBox struct {
	s        *Service
	ctx      context.Context
	room     contract.RoomID
	session  contract.SessionID
	owner    contract.Principal
	id       contract.BoxID
	host     *podman.Host
	provider *toolUpstream
}

// DYNA_BOX_TEST_IMAGE names a local dyna-base image. DYNA_BOX_TEST_BINARY names a static dyna build.
func newToolBox(t *testing.T, profiles Profiles, provider *toolUpstream) *toolBox {
	t.Helper()
	image, binary := os.Getenv("DYNA_BOX_TEST_IMAGE"), os.Getenv("DYNA_BOX_TEST_BINARY")
	if image == "" || binary == "" {
		t.Skip("DYNA_BOX_TEST_IMAGE and DYNA_BOX_TEST_BINARY name a dyna-base image and a dyna build; this test runs a real box")
	}
	s, room, session, base := setup(t)
	s.profiles = profiles
	s.boxes = &dormantHost{}
	resetLifecycle(s)
	ctx, cancel := context.WithTimeout(base, 8*time.Minute)
	t.Cleanup(cancel)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	data, err := os.MkdirTemp("", "dyna-bash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: filepath.Join(data, "host")}
	s.SetLayout(layout)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	id := record.Box
	server := api.New(boxService{Service: s}, func(api.Caller) http.Handler { return http.HandlerFunc(provider.serve) })
	runtimeDir := filepath.Join(data, "rt")
	sockets := api.NewSockets(server, runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	if err := sockets.AddBox(id, session, room); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.HostSocketFolder(id), 0755); err != nil {
		t.Fatal(err)
	}
	slowRelay(t, layout.HostSocket(id), filepath.Join(runtimeDir, "boxes", string(id)+".sock"), 256<<20)
	bin := filepath.Join(layout.HostBin(), "current")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", binary, filepath.Join(bin, "dyna")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	host, err := podman.New("local", runtimeDir, "h", localRunner{})
	if err != nil {
		t.Fatal(err)
	}
	slice := fmt.Sprintf("dyna-bash-test-%d.slice", time.Now().UnixNano())
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("podman", "logs", "--tail", "40", string(id)).CombinedOutput()
			t.Logf("box log:\n%s", out)
		}
		_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
		_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
		_ = host.RemoveSlice(context.Background(), slice)
	})
	if err := host.EnsureSlice(ctx, box.Slice{Name: slice, CPUs: 2, MemHigh: 2 << 30, MemMax: 3 << 30, Pids: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := host.CreateContainer(ctx, box.ContainerSpec{Box: id, Image: image, Slice: slice, Labels: []box.Label{{Key: "dyna.layout", Value: box.ContainerLayout}}, Mounts: []box.Mount{
		{HostPath: layout.HostSocketFolder(id), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: layout.HostBin(), BoxPath: "/opt/dyna/bin", ReadOnly: true},
		{HostPath: filepath.Dir(layout.HostInstructions("")), BoxPath: "/run/dyna/instructions/global", ReadOnly: true, MakeDir: true},
		{HostPath: filepath.Dir(layout.HostInstructions(room)), BoxPath: "/run/dyna/instructions/room", ReadOnly: true, MakeDir: true},
	}}); err != nil {
		t.Fatal(err)
	}
	return &toolBox{s, ctx, room, session, owner, id, host, provider}
}

func (b *toolBox) start(t *testing.T) {
	t.Helper()
	if err := b.host.StartContainer(b.ctx, b.id); err != nil {
		t.Fatal(err)
	}
}

func (b *toolBox) turns(t *testing.T) int {
	t.Helper()
	listed, problem := b.s.EventsList(b.ctx, b.owner, contract.EventsListRequest{Room: b.room, Types: []contract.EventType{"turn.ended"}, Limit: 1000})
	if problem != nil {
		t.Fatal(problem)
	}
	ended := 0
	for _, event := range listed.Events {
		if body := event.Body.(contract.TurnEnded); body.Session == b.session {
			if body.Outcome != contract.TurnCompleted {
				t.Fatalf("turn %+v", body)
			}
			ended++
		}
	}
	return ended
}

func (b *toolBox) waitTurns(t *testing.T, what string, want int) {
	t.Helper()
	for b.turns(t) < want {
		if b.ctx.Err() != nil {
			t.Fatalf("%s: waited for %d turns, have %d", what, want, b.turns(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// task posts one task and waits for its turn to end.
func (b *toolBox) task(t *testing.T, name string) []toolResult {
	t.Helper()
	before := b.turns(t)
	if _, problem := b.s.Post(b.ctx, b.owner, contract.PostRequest{Room: b.room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: b.session}, Text: name}); problem != nil {
		t.Fatal(problem)
	}
	b.waitTurns(t, name, before+1)
	return b.provider.results(t)
}

// A session in a real box on real Podman, with the real box agent and the Pi the image ships, against the fake upstream: bash in
// the box and on a host, directly and from a script, its background jobs and the wake when they end, masking, the job tool, a
// refusal without a grant, and the jobs as the owner's panel sees them.
func TestBashInARealBox(t *testing.T) {
	bash := func(args map[string]any) crewTool { return crewTool{"bash", jsonOf(t, args)} }
	script := func(code string) crewTool { return crewTool{"codemode", jsonOf(t, map[string]string{"code": code})} }
	provider := &toolUpstream{tools: map[string][]crewTool{
		"task-run":      {bash(map[string]any{"command": "echo hello; pwd"})},
		"task-mask":     {bash(map[string]any{"command": "echo secret=abc123"})},
		"task-fail":     {bash(map[string]any{"command": "echo broken; exit 3"})},
		"task-cwd":      {bash(map[string]any{"command": "true", "cwd": "no\u200bpe"})},
		"task-long":     {bash(map[string]any{"command": "sleep 40; echo finished box", "description": "Slow in the box"})},
		"task-jobs":     {{"job", `{"action":"list"}`}},
		"task-big":      {bash(map[string]any{"command": "seq 1 20000"})},
		"task-host":     {bash(map[string]any{"command": "echo on-host $((1+1)) | cat; echo secret=abc123", "host": "local"})},
		"task-hostfail": {bash(map[string]any{"command": "exit 5", "host": "local"})},
		"task-refused":  {bash(map[string]any{"command": "true", "host": "elsewhere"})},
		"task-hostlong": {bash(map[string]any{"command": "sleep 40; echo finished host", "host": "local", "description": "Slow on a host"})},
		"task-script": {script(`const box = await tools.bash({ command: "echo script-box; exit 4" });
const host = await tools.bash({ command: "echo script-host", host: "local" });
let refused = "";
try { await tools.bash({ command: "true", host: "elsewhere" }); } catch (error) { refused = error.message; }
const jobs = await tools.job({ action: "list" });
return JSON.stringify({ box, host, refused, jobs: jobs.jobs.length });`)},
	}}
	b := newToolBox(t, boxProfiles{}, provider)
	s, ctx, room, session, owner, id := b.s, b.ctx, b.room, b.session, b.owner, b.id
	who := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	s.SetHostexecRunner(localHostexec{})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	grant := contract.Grant{ID: "grant_bash", Kind: contract.GrantHostExec, Room: room, To: who, By: owner, Created: s.now(), Hosts: []contract.HostName{"local"}}
	if err := s.store.CreateGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	turns := func() int { return b.turns(t) }
	waitTurns := func(what string, want int) { t.Helper(); b.waitTurns(t, what, want) }
	task := func(name string) []toolResult { t.Helper(); return b.task(t, name) }
	last := func(results []toolResult) toolResult { return results[len(results)-1] }

	result := last(task("task-run"))
	if result.IsError || !strings.Contains(result.Text, "hello\n/box/work\n") || !strings.Contains(result.Text, ": exit 0]") {
		t.Fatalf("bash: %+v", result)
	}
	names := strings.Join(provider.declared(), " ")
	if !strings.Contains(" "+names+" ", " bash ") || !strings.Contains(" "+names+" ", " job ") || strings.Contains(names, "shell") {
		t.Fatalf("declared tools: %s", names)
	}

	// The owner's panel watches the box from here on.
	var jobsMu sync.Mutex
	jobs := map[contract.ShellJobID]contract.ShellJobOutput{}
	watching, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	views, problem := s.ShellWatch(watching, session)
	if problem != nil {
		t.Fatal(problem)
	}
	go func() {
		for view := range views {
			jobsMu.Lock()
			jobs[view.Job] = view
			jobsMu.Unlock()
		}
	}()
	seen := func(what string, match func(contract.ShellJobOutput) bool) contract.ShellJobOutput {
		t.Helper()
		for {
			jobsMu.Lock()
			for _, view := range jobs {
				if match(view) {
					jobsMu.Unlock()
					return view
				}
			}
			jobsMu.Unlock()
			if ctx.Err() != nil {
				t.Fatalf("the panel never showed %s: %+v", what, jobs)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	seen("the first job", func(view contract.ShellJobOutput) bool { return strings.HasPrefix(view.Command, "echo hello") })

	result = last(task("task-mask"))
	if strings.Contains(result.Text, "abc123") || !strings.Contains(result.Text, "secret=[masked]") {
		t.Fatalf("masking: %+v", result)
	}
	result = last(task("task-fail"))
	if result.IsError || !strings.Contains(result.Text, "broken") || !strings.Contains(result.Text, ": exit 3]") {
		t.Fatalf("a nonzero exit: %+v", result)
	}
	result = last(task("task-cwd"))
	if !result.IsError || !strings.Contains(result.Text, `cwd "/box/work/no\u200bpe" does not exist`) {
		t.Fatalf("a bad cwd: %+v", result)
	}

	result = last(task("task-big"))
	full := regexp.MustCompile(`Full output: (\S+)`).FindStringSubmatch(result.Text)
	if full == nil || len(result.Text) > 21<<10 || !strings.Contains(result.Text, "\n20000\n") || strings.Contains(result.Text, "\n1\n") {
		t.Fatalf("a long output: %d bytes: %.200s", len(result.Text), result.Text)
	}
	if out, err := exec.Command("podman", "exec", string(id), "wc", "-l", full[1]).CombinedOutput(); err != nil || !strings.HasPrefix(string(out), "20000 ") {
		t.Fatalf("the full output: %v: %s", err, out)
	}

	jobID := regexp.MustCompile(`job_[0-9a-f]+`)
	endedEvents := func() []contract.ShellJobEnded {
		listed, problem := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room, Types: []contract.EventType{"shell.job.ended"}, Limit: 1000})
		if problem != nil {
			t.Fatal(problem)
		}
		var ended []contract.ShellJobEnded
		for _, event := range listed.Events {
			ended = append(ended, event.Body.(contract.ShellJobEnded))
		}
		return ended
	}
	long := func(name, finished string, wantHost contract.HostName) {
		t.Helper()
		before, waking := turns(), len(endedEvents())
		start := time.Now()
		result := last(task(name))
		if took := time.Since(start); took < 29*time.Second || took > 33*time.Second+10*time.Second {
			t.Fatalf("%s: the turn took %v, not the foreground limit", name, took)
		}
		if result.IsError || !strings.Contains(result.Text, "] The job is in the background. You can end your turn and Dyna will wake you when it completes.") {
			t.Fatalf("%s: %+v", name, result)
		}
		job := contract.ShellJobID(jobID.FindString(result.Text))
		view := seen("a running job", func(view contract.ShellJobOutput) bool { return view.Job == job && view.Running })
		if view.Host != wantHost {
			t.Fatalf("%s: panel %+v", name, view)
		}
		waitTurns(name+"'s wake", before+2)
		ended := endedEvents()
		if len(ended) != waking+1 {
			t.Fatalf("%s: %d ends after %d: %+v", name, len(ended), waking, ended)
		}
		if got := ended[len(ended)-1]; got.Job != job || got.End.Kind != contract.ShellJobExited || got.End.Code != 0 || !strings.Contains(got.Tail, finished) {
			t.Fatalf("%s: end %+v", name, got)
		}
		if body := provider.lastBody(); !strings.Contains(body, string(job)) || !strings.Contains(body, finished) {
			t.Fatalf("%s: the wake's input does not name the job: %s", name, body)
		}
		seen("the job's end", func(view contract.ShellJobOutput) bool {
			return view.Job == job && !view.Running && strings.Contains(view.Text, finished)
		})
	}
	long("task-long", "finished box", "")

	result = last(task("task-jobs"))
	if result.IsError || !strings.Contains(result.Text, "exit 0\techo hello") {
		t.Fatalf("job list: %+v", result)
	}

	result = last(task("task-host"))
	if result.IsError || !strings.Contains(result.Text, "on-host 2") || strings.Contains(result.Text, "abc123") || !strings.Contains(result.Text, ": exit 0]") {
		t.Fatalf("a host run: %+v", result)
	}
	if view := seen("the host run", func(view contract.ShellJobOutput) bool { return strings.HasPrefix(view.Command, "echo on-host") }); view.Host != "local" || strings.Contains(view.Text, "abc123") {
		t.Fatalf("host job: %+v", view)
	}
	if got := s.storeCount(t, "SELECT COUNT(*) FROM hostexec_runs"); got != 1 {
		t.Fatalf("%d hostexec_runs", got)
	}
	result = last(task("task-hostfail"))
	if result.IsError || !strings.Contains(result.Text, ": exit 5]") {
		t.Fatalf("a nonzero exit on a host: %+v", result)
	}
	result = last(task("task-refused"))
	if !result.IsError || !strings.Contains(result.Text, "no_grant") {
		t.Fatalf("no grant: %+v", result)
	}
	if got := s.storeCount(t, "SELECT COUNT(*) FROM hostexec_runs"); got != 2 {
		t.Fatalf("a refusal ran something: %d hostexec_runs", got)
	}
	long("task-hostlong", "finished host", "local")

	result = last(task("task-script"))
	var out struct {
		Box struct {
			Output   string `json:"output"`
			ExitCode int    `json:"exit_code"`
			Job      string `json:"job"`
		} `json:"box"`
		Host struct {
			Output string `json:"output"`
		} `json:"host"`
		Refused string `json:"refused"`
		Jobs    int    `json:"jobs"`
	}
	text := result.Text[strings.Index(result.Text, "{") : strings.LastIndex(result.Text, "}")+1]
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%v: %+v", err, result)
	}
	if out.Box.Output != "script-box\n" || out.Box.ExitCode != 4 || out.Box.Job == "" || out.Host.Output != "script-host\n" || !strings.HasPrefix(out.Refused, "no_grant") || out.Jobs < 8 {
		t.Fatalf("codemode: %+v", out)
	}
	seen("a script's job", func(view contract.ShellJobOutput) bool {
		return view.Command == "echo script-host" && view.Host == "local"
	})
}

func (s *Service) storeCount(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := s.store.Read().QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
