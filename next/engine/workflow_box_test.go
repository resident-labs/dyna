//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/model"
)

const (
	crewFastModel = "claude-opus-4-5"
	crewDeepModel = "claude-sonnet-4-5"
)

// crewProfiles offers two profiles with different models, so a crew must reach both through one session's route.
type crewProfiles struct{ fakeProfiles }

func (crewProfiles) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	id := map[contract.ProfileName]string{"test": crewFastModel, "fast": crewFastModel, "deep": crewDeepModel}[name]
	if id == "" {
		return contract.ModelChoice{}, nil, fmt.Errorf("unknown profile %q", name)
	}
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: id, Thinking: contract.ThinkingOff}, []string{id}, nil
}

type crewRequest struct{ task, model, affinity string }

// crewUpstream stands where the model proxy would be, behind the real route. Each worker's prompt names a task, and a task
// answers when its gate opens. A task named refuse fails as a provider refusal does.
type crewUpstream struct {
	mu       sync.Mutex
	requests []crewRequest
	gates    map[string]chan struct{}
}

var crewTask = regexp.MustCompile(`(task|refuse)-[a-z0-9]+`)

func (m *crewUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(data, &body)
	task := crewTask.FindString(string(body.Messages))
	m.mu.Lock()
	m.requests = append(m.requests, crewRequest{task, body.Model, r.Header.Get("X-Claude-Code-Session-Id")})
	gate := m.gates[task]
	m.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	if strings.HasPrefix(task, "refuse") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"the provider refused this"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"found %s"}}`, task))
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

func (m *crewUpstream) hold(tasks ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, task := range tasks {
		m.gates[task] = make(chan struct{})
	}
}

func (m *crewUpstream) release(tasks ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, task := range tasks {
		if gate := m.gates[task]; gate != nil {
			close(gate)
			delete(m.gates, task)
		}
	}
}

// since lists the tasks asked for after the first n requests.
func (m *crewUpstream) since(n int) []crewRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests[n:])
}

func (m *crewUpstream) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

const crewScript = `export const meta = {
	name: 'audit',
	description: 'Eleven workers on two profiles in one box',
	profiles: ['fast', 'deep'],
	size: 'medium',
}
phase('audit')
const tasks = ['task-a', 'task-b', 'task-c', 'task-d', 'task-e', 'task-f', 'task-g', 'task-h', 'task-i', 'task-j', 'refuse-k']
const results = await parallel(tasks.map((task, i) => () => agent('Work on ' + task, { profile: i % 2 ? 'deep' : 'fast', label: task })))
log('all workers came back')
return { results }
`

// One crew in one real box, on real Podman, with the real box agent, the real route and the real Pi the image ships. Eleven
// workers on two profiles run in one container; the box is destroyed with three still working, and a new box finishes the
// script from the journal; an edited script reruns one step; an unknown profile costs nothing.
//
// DYNA_BOX_TEST_IMAGE names a local dyna-base image. DYNA_BOX_TEST_BINARY names a static dyna build.
func TestWorkflowInARealBox(t *testing.T) {
	image, binary := os.Getenv("DYNA_BOX_TEST_IMAGE"), os.Getenv("DYNA_BOX_TEST_BINARY")
	if image == "" || binary == "" {
		t.Skip("DYNA_BOX_TEST_IMAGE and DYNA_BOX_TEST_BINARY name a dyna-base image and a dyna build; this test runs a real box")
	}
	s, room, _, base := setup(t)
	s.profiles = crewProfiles{}
	ctx, cancel := context.WithTimeout(base, 15*time.Minute)
	defer cancel()
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND parent IS NULL", room).Scan(&root); err != nil {
		t.Fatal(err)
	}
	data, err := os.MkdirTemp("", "dyna-wf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: filepath.Join(data, "host")}
	s.SetLayout(layout)

	// The upstream listens on a socket, as the proxy does, and the route in front of it is the engine's.
	upstream := &crewUpstream{gates: map[string]chan struct{}{}}
	proxySocket := filepath.Join(data, "proxy.sock")
	proxyListener, err := net.Listen("unix", proxySocket)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &http.Server{Handler: upstream}
	go proxy.Serve(proxyListener)
	t.Cleanup(func() { _ = proxy.Close() })
	server := api.New(boxService{Service: s}, func(c api.Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := s.store.Session(r.Context(), c.Principal.Session)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			allowed, crew, err := s.AllowedModels(r.Context(), session)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			model.Handler(proxySocket, "client-key", string(session.ID), allowed, []string{"account"}, "", crew).ServeHTTP(w, r)
		})
	})
	server.SetFilesRoute(func(c api.Caller) http.Handler { return s.Files(c.Principal, c.Room) })
	runtimeDir := filepath.Join(data, "rt")
	sockets := api.NewSockets(server, runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
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

	containers := func(label string) []string {
		t.Helper()
		out, err := exec.Command("podman", "ps", "-a", "--filter", "label="+label, "--format", "{{.Names}}").CombinedOutput()
		if err != nil {
			t.Fatalf("podman ps: %v: %s", err, out)
		}
		return strings.Fields(string(out))
	}
	var created []contract.BoxID
	destroy := func(id contract.BoxID) {
		_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
		_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
	}
	t.Cleanup(func() {
		for _, id := range created {
			if t.Failed() {
				out, _ := exec.Command("podman", "logs", "--tail", "40", string(id)).CombinedOutput()
				t.Logf("box %s log:\n%s", id, out)
			}
			destroy(id)
			if slice, err := box.SliceName(id); err == nil {
				_ = host.RemoveSlice(context.Background(), slice)
			}
		}
	})
	// boot gives a box its container and volume, as a host does.
	boot := func(id contract.BoxID) {
		t.Helper()
		slice, err := box.SliceName(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := host.EnsureSlice(ctx, box.Slice{Name: slice, CPUs: 8, MemHigh: 8 << 30, MemMax: 12 << 30, Pids: 4096}); err != nil {
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
		if err := host.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// start runs a script and gives its crew a box.
	start := func(caller contract.Principal, script string, resume contract.WorkflowRunID) contract.WorkflowRunResponse {
		t.Helper()
		started, problem := s.WorkflowRun(ctx, caller, contract.WorkflowRunRequest{Room: room, Script: script, Resume: resume})
		if problem != nil {
			t.Fatal(problem)
		}
		passLifecycle(t, s, ctx)
		if err := sockets.AddBox(started.Box, started.Session, room); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(layout.HostSocketFolder(started.Box), 0755); err != nil {
			t.Fatal(err)
		}
		slowRelay(t, layout.HostSocket(started.Box), filepath.Join(runtimeDir, "boxes", string(started.Box)+".sock"), 64<<20)
		created = append(created, started.Box)
		boot(started.Box)
		return started
	}
	wait := func(what string, done func() bool) {
		t.Helper()
		for !done() {
			if ctx.Err() != nil {
				t.Fatalf("waited for %s, after %d model requests", what, upstream.count())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	status := func(run contract.WorkflowRunID) contract.WorkflowStatusResponse {
		t.Helper()
		got, problem := s.WorkflowStatus(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.WorkflowStatusRequest{Run: run})
		if problem != nil {
			t.Fatal(problem)
		}
		return got
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := s.store.Read().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	doneSteps := func(run contract.WorkflowRunID) int {
		return count("SELECT COUNT(*) FROM workflow_steps WHERE run=? AND kind='agent' AND state='done'", run)
	}

	// An unknown profile fails before a box or a model call exists.
	sessionsBefore, boxesBefore := count("SELECT COUNT(*) FROM sessions"), count("SELECT COUNT(*) FROM boxes")
	unknown := strings.Replace(crewScript, "'deep']", "'no-such-profile']", 1)
	if _, problem := s.WorkflowRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.WorkflowRunRequest{Room: room, Script: unknown}); problem == nil || !strings.Contains(problem.Message, "no-such-profile") {
		t.Fatalf("unknown profile: %+v", problem)
	}
	if upstream.count() != 0 || count("SELECT COUNT(*) FROM sessions") != sessionsBefore || count("SELECT COUNT(*) FROM boxes") != boxesBefore || count("SELECT COUNT(*) FROM workflow_runs") != 0 {
		t.Fatal("an unknown profile left something behind")
	}

	// Every worker waits at the model, so all eleven are running at once.
	all := []string{"task-a", "task-b", "task-c", "task-d", "task-e", "task-f", "task-g", "task-h", "task-i", "task-j", "refuse-k"}
	unfinished := []string{"task-i", "task-j", "refuse-k"}
	upstream.hold(all...)
	first := start(contract.Principal{Kind: contract.PrincipalSession, Session: root}, crewScript, "")
	wait("eleven workers at the model", func() bool { return upstream.count() >= len(all) })
	t.Logf("eleven requests held after the box started")
	inBox := func(id contract.BoxID, script string) string {
		t.Helper()
		out, err := exec.Command("podman", "exec", string(id), "bash", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("in the box, %s: %v: %s", script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := inBox(first.Box, "pgrep -x pi | wc -l"); got != "11" {
		t.Fatalf("%s Pi processes in the box, want 11", got)
	}
	if got := containers("dyna.box"); !slices.Equal(got, []string{string(first.Box)}) {
		t.Fatalf("containers %v", got)
	}
	if count("SELECT COUNT(*) FROM sessions") != sessionsBefore+1 || count("SELECT COUNT(*) FROM boxes") != boxesBefore+1 {
		t.Fatal("workers took session or box rows")
	}

	// Each worker keeps its own affinity under the crew's session, and each profile's model passes the route.
	byTask := map[string]crewRequest{}
	affinities := map[string]bool{}
	for _, request := range upstream.since(0) {
		byTask[request.task] = request
		affinities[request.affinity] = true
		if !strings.HasPrefix(request.affinity, string(first.Session)+"/") {
			t.Fatalf("affinity %q is not the crew's", request.affinity)
		}
	}
	if len(affinities) != len(all) {
		t.Fatalf("%d affinities for %d workers", len(affinities), len(all))
	}
	if byTask["task-a"].model != crewFastModel || byTask["task-b"].model != crewDeepModel {
		t.Fatalf("models %+v", byTask)
	}

	// Eight finish. The box is destroyed with its volume while three still work, and a new one takes its place.
	upstream.release(slices.DeleteFunc(slices.Clone(all), func(task string) bool { return slices.Contains(unfinished, task) })...)
	wait("eight stored steps", func() bool { return doneSteps(first.Run) == 8 })
	destroy(first.Box)
	upstream.release(unfinished...)
	before := upstream.count()
	// The way a lost box comes back: the host reports it gone, it fails, and the parent restarts the session.
	lost, err := s.store.Box(ctx, first.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, room, lost, contract.BoxFailed, "the container is gone", ""); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.SessionRestartRequest{Session: first.Session}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	boot(first.Box)

	wait("the run's end", func() bool { return status(first.Run).Run.State != contract.WorkflowRunning })
	final := status(first.Run)
	asked := map[string]bool{}
	for _, request := range upstream.since(before) {
		asked[request.task] = true
	}
	if len(asked) != len(unfinished) || !asked["task-i"] || !asked["task-j"] || !asked["refuse-k"] {
		t.Fatalf("the new box asked the model for %v, want only %v", asked, unfinished)
	}

	// The failed worker is in the result and the end event, and the caller got a final reply that names it.
	if final.Run.State != contract.WorkflowDone || final.Run.Failed != 1 || !slices.Equal(final.Run.FailedSteps, []string{"refuse-k"}) {
		t.Fatalf("run %+v", final.Run)
	}
	var result struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(final.Run.Result), &result); err != nil {
		t.Fatalf("result %q: %v", final.Run.Result, err)
	}
	var entry struct {
		Failed bool
		Step   string
		Reason string
	}
	if len(result.Results) != len(all) || string(result.Results[0]) != `"found task-a"` || string(result.Results[9]) != `"found task-j"` {
		t.Fatalf("result %s", final.Run.Result)
	}
	if err := json.Unmarshal(result.Results[10], &entry); err != nil || !entry.Failed || entry.Step != "refuse-k" || !strings.Contains(entry.Reason, "refused") {
		t.Fatalf("failed entry %s: %v", result.Results[10], err)
	}
	listed, problem := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"workflow.ended", "turn.ended"}, Limit: 100})
	if problem != nil {
		t.Fatal(problem)
	}
	var ended *contract.WorkflowEnded
	var reply *contract.TurnEnded
	for _, event := range listed.Events {
		switch body := event.Body.(type) {
		case contract.WorkflowEnded:
			ended = &body
		case contract.TurnEnded:
			if body.Session == first.Session {
				reply = &body
			}
		}
	}
	if ended == nil || ended.State != contract.WorkflowDone || ended.Failed != 1 || !slices.Equal(ended.FailedSteps, []string{"refuse-k"}) {
		t.Fatalf("end event %+v", ended)
	}
	if reply == nil || reply.Outcome != contract.TurnCompleted || !strings.Contains(reply.Final, "found task-a") || !strings.Contains(reply.Final, "Failed steps (1): refuse-k") {
		t.Fatalf("reply %+v", reply)
	}
	if len(reply.Replies) != 1 || reply.Replies[0].Reply.Target.Session != root {
		t.Fatalf("reply went to %+v", reply.Replies)
	}
	if count("SELECT COUNT(*) FROM deliveries WHERE to_id=? AND wake=1 AND seq IN (SELECT seq FROM events WHERE type='turn.ended')", root) != 1 {
		t.Fatal("the caller got no delivery of the result")
	}
	if want := int64(10 * (len(all) - 1)); reply.Usage.InputTokens != want {
		t.Fatalf("crew usage %+v, want %d input tokens", reply.Usage, want)
	}
	if got := count("SELECT input_tokens FROM turns WHERE session=?", first.Session); got != int(reply.Usage.InputTokens) {
		t.Fatalf("turn row has %d input tokens", got)
	}

	// Every finished worker's transcript reached the engine, filed under the crew's one turn.
	files := map[string]bool{}
	for _, step := range final.Steps {
		if step.Kind == contract.WorkflowStepAgent && step.State == contract.WorkflowStepDone {
			if step.Transcript == "" || files[step.Transcript] {
				t.Fatalf("step %+v", step)
			}
			files[step.Transcript] = true
			wait("transcript "+step.Transcript, func() bool {
				info, err := os.Stat(filepath.Join(layout.Transcripts(room, first.Session), step.Transcript))
				return err == nil && info.Size() > 0
			})
			if count("SELECT COUNT(*) FROM transcript_turns WHERE session=? AND path=?", first.Session, step.Transcript) != 1 {
				t.Fatalf("transcript %s has no turn", step.Transcript)
			}
		}
	}
	if len(files) != 10 {
		t.Fatalf("%d transcripts", len(files))
	}
	if got := count("SELECT COUNT(DISTINCT turn) FROM transcript_turns WHERE session=?", first.Session); got != 1 {
		t.Fatalf("transcripts span %d turns", got)
	}
	if got := count("SELECT COUNT(*) FROM boxes WHERE session=?", first.Session); got != 1 {
		t.Fatalf("%d box rows after the replacement", got)
	}
	// The crew closes like any session: its box sends the last transcript bytes and reports its archive.
	wait("the crew's close", func() bool {
		passLifecycle(t, s, ctx)
		return count("SELECT COUNT(*) FROM sessions WHERE id=? AND closed_at IS NOT NULL", first.Session) == 1
	})

	// The owner edits one prompt and drops the refused task, and resumes the run. Only the edited step asks the model.
	edited := strings.Replace(crewScript, "'task-c'", "'task-cc'", 1)
	edited = strings.Replace(edited, ", 'refuse-k'", "", 1)
	before = upstream.count()
	second := start(contract.Principal{Kind: contract.PrincipalOwner}, edited, first.Run)
	wait("the edited run's end", func() bool { return status(second.Run).Run.State != contract.WorkflowRunning })
	requests := upstream.since(before)
	if len(requests) != 1 || requests[0].task != "task-cc" {
		t.Fatalf("the edited run asked the model for %+v", requests)
	}
	if got := status(second.Run); got.Run.State != contract.WorkflowDone || got.Run.Failed != 0 || !strings.Contains(got.Run.Result, "found task-cc") || !strings.Contains(got.Run.Result, "found task-a") {
		t.Fatalf("edited run %+v", got.Run)
	}
	if got := count("SELECT COUNT(*) FROM sessions"); got != sessionsBefore+2 {
		t.Fatalf("%d sessions after two runs, %d before", got, sessionsBefore)
	}
}
