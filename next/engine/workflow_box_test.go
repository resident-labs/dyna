//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	enginegit "github.com/resident-labs/dyna/engine/git"
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

// crewTool is one tool call a task's worker makes, with its arguments as JSON.
type crewTool struct{ name, input string }

// crewUpstream stands where the model proxy would be, behind the real route. Each worker's prompt names a task, and a task
// answers when its gate opens: first with its tools, one a request, and then with text. A task named refuse fails as a
// provider refusal does.
type crewUpstream struct {
	mu       sync.Mutex
	requests []crewRequest
	gates    map[string]chan struct{}
	tools    map[string][]crewTool
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
	m.mu.Lock()
	tools := m.tools[task]
	m.mu.Unlock()
	if done := strings.Count(string(body.Messages), `"tool_result"`); done < len(tools) {
		input, _ := json.Marshal(tools[done].input)
		send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_%d","name":%q,"input":{}}}`, done, tools[done].name))
		send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, input))
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
		send("message_stop", `{"type":"message_stop"}`)
		return
	}
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

// realCrew is what a crew in a real box needs: real Podman, the real box agent, the real routes and the real Pi the image ships,
// with a model upstream that follows a script. Its repository, when it has one, is a bare one served by git itself, behind the
// engine's real git route.
//
// DYNA_BOX_TEST_IMAGE names a local dyna-base image. DYNA_BOX_TEST_BINARY names a static dyna build.
type realCrew struct {
	t        *testing.T
	s        *Service
	ctx      context.Context
	room     contract.RoomID
	root     contract.SessionID
	layout   box.Layout
	upstream *crewUpstream
	host     *podman.Host
	sockets  *api.Sockets
	runtime  string
	image    string
	origin   string // the repository's bare remote, when the room has one
	cache    string // what the host would keep of it, which a box clones from
	created  []contract.BoxID
}

// crewRepos stands for the mirror, which fetches from GitHub.
type crewRepos struct{ origin, cache string }

func (r crewRepos) Get(ctx context.Context, session contract.SessionID, _ contract.HostName, fetch bool) ([]contract.BoxRepo, error) {
	if fetch {
		if out, err := exec.CommandContext(ctx, "git", "-C", r.cache, "fetch", "--prune", r.origin, "+refs/heads/*:refs/heads/*").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("%w: %s", err, out)
		}
	}
	return []contract.BoxRepo{{Name: "owner/repo", Cache: r.cache, Branch: "v2/" + string(session), Base: "main"}}, nil
}

func (r crewRepos) Ref(ctx context.Context, _ contract.RepoName, ref string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", r.origin, "rev-parse", ref).Output()
	return strings.TrimSpace(string(out)), err
}

func (crewRepos) DeleteArchiveRef(context.Context, contract.RepoName, string, string) error {
	return nil
}

func (crewRepos) Size(context.Context, contract.RepoName) (contract.BoxSizeName, error) {
	return "", nil
}

func newRealCrew(t *testing.T, files map[string]string) *realCrew {
	t.Helper()
	image, binary := os.Getenv("DYNA_BOX_TEST_IMAGE"), os.Getenv("DYNA_BOX_TEST_BINARY")
	if image == "" || binary == "" {
		t.Skip("DYNA_BOX_TEST_IMAGE and DYNA_BOX_TEST_BINARY name a dyna-base image and a dyna build; this test runs a real box")
	}
	s, room, _, base := setup(t)
	s.profiles = crewProfiles{}
	ctx, cancel := context.WithTimeout(base, 15*time.Minute)
	t.Cleanup(cancel)
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
	upstream := &crewUpstream{gates: map[string]chan struct{}{}, tools: map[string][]crewTool{}}
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
	c := &realCrew{t: t, s: s, ctx: ctx, room: room, root: root, layout: layout, upstream: upstream, runtime: filepath.Join(data, "rt"), image: image}
	if files != nil {
		c.addRepo(server, filepath.Join(data, "git"), files)
	}
	c.sockets = api.NewSockets(server, c.runtime, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = c.sockets.Close() })
	bin := filepath.Join(layout.HostBin(), "current")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", binary, filepath.Join(bin, "dyna")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if c.host, err = podman.New("local", c.runtime, "h", localRunner{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range c.created {
			if t.Failed() {
				out, _ := exec.Command("podman", "logs", "--tail", "40", string(id)).CombinedOutput()
				t.Logf("box %s log:\n%s", id, out)
			}
			c.destroy(id)
			if slice, err := box.SliceName(id); err == nil {
				_ = c.host.RemoveSlice(context.Background(), slice)
			}
		}
	})
	return c
}

// git runs git on the test's side, in dir.
func (c *realCrew) git(dir string, args ...string) string {
	c.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=seed", "GIT_AUTHOR_EMAIL=seed@dyna.invalid", "GIT_COMMITTER_NAME=seed", "GIT_COMMITTER_EMAIL=seed@dyna.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		c.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// addRepo gives the room a repository holding files, behind the engine's own route.
func (c *realCrew) addRepo(server *api.API, root string, files map[string]string) {
	c.t.Helper()
	c.origin, c.cache = filepath.Join(root, "owner", "repo.git"), filepath.Join(root, "cache", "owner", "repo.git")
	seed := filepath.Join(root, "seed")
	for _, dir := range []string{filepath.Dir(c.origin), filepath.Dir(c.cache), seed} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			c.t.Fatal(err)
		}
	}
	c.git(root, "init", "--bare", "-b", "main", c.origin)
	c.git(c.origin, "config", "http.receivepack", "true")
	c.git(root, "init", "--bare", "-b", "main", c.cache)
	c.git(seed, "init", "-b", "main")
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(text), 0644); err != nil {
			c.t.Fatal(err)
		}
	}
	c.git(seed, "add", "-A")
	c.git(seed, "commit", "-m", "start")
	c.git(seed, "push", c.origin, "main")
	backend := &cgi.Handler{Path: filepath.Join(c.git(root, "--exec-path"), "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	remote := httptest.NewServer(backend)
	c.t.Cleanup(remote.Close)
	upstream, err := url.Parse(remote.URL)
	if err != nil {
		c.t.Fatal(err)
	}
	server.SetGitRoute(func(caller api.Caller) http.Handler {
		return enginegit.Handler(upstream, http.DefaultTransport, "test-token", caller.Principal.Session, caller.Room, c.s.store)
	})
	if err := c.s.store.AddRoomRepo(c.ctx, c.room, "owner/repo", true); err != nil {
		c.t.Fatal(err)
	}
	c.s.SetRepositories(crewRepos{c.origin, c.cache})
	c.s.SetGitIdentity("Crew Test", "crew@dyna.invalid")
}

func (c *realCrew) containers(label string) []string {
	c.t.Helper()
	out, err := exec.Command("podman", "ps", "-a", "--filter", "label="+label, "--format", "{{.Names}}").CombinedOutput()
	if err != nil {
		c.t.Fatalf("podman ps: %v: %s", err, out)
	}
	return strings.Fields(string(out))
}

func (c *realCrew) destroy(id contract.BoxID) {
	_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
	_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
}

// boot gives a box its container and volume, as a host does.
func (c *realCrew) boot(id contract.BoxID) {
	c.t.Helper()
	slice, err := box.SliceName(id)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.host.EnsureSlice(c.ctx, box.Slice{Name: slice, CPUs: 8, MemHigh: 8 << 30, MemMax: 12 << 30, Pids: 4096}); err != nil {
		c.t.Fatal(err)
	}
	mounts := []box.Mount{
		{HostPath: c.layout.HostSocketFolder(id), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: c.layout.HostBin(), BoxPath: "/opt/dyna/bin", ReadOnly: true},
		{HostPath: filepath.Dir(c.layout.HostInstructions("")), BoxPath: "/run/dyna/instructions/global", ReadOnly: true, MakeDir: true},
		{HostPath: filepath.Dir(c.layout.HostInstructions(c.room)), BoxPath: "/run/dyna/instructions/room", ReadOnly: true, MakeDir: true},
	}
	if c.cache != "" {
		mounts = append(mounts, box.Mount{HostPath: c.cache, BoxPath: c.cache, ReadOnly: true})
	}
	if err := c.host.CreateContainer(c.ctx, box.ContainerSpec{Box: id, Image: c.image, Slice: slice, Labels: []box.Label{{Key: "dyna.layout", Value: box.ContainerLayout}}, Mounts: mounts}); err != nil {
		c.t.Fatal(err)
	}
	if err := c.host.StartContainer(c.ctx, id); err != nil {
		c.t.Fatal(err)
	}
}

// start runs a script and gives its crew a box.
func (c *realCrew) start(caller contract.Principal, script string, resume contract.WorkflowRunID) contract.WorkflowRunResponse {
	c.t.Helper()
	started, problem := c.s.WorkflowRun(c.ctx, caller, contract.WorkflowRunRequest{Room: c.room, Script: script, Resume: resume})
	if problem != nil {
		c.t.Fatal(problem)
	}
	passLifecycle(c.t, c.s, c.ctx)
	if err := c.sockets.AddBox(started.Box, started.Session, c.room); err != nil {
		c.t.Fatal(err)
	}
	if err := os.MkdirAll(c.layout.HostSocketFolder(started.Box), 0755); err != nil {
		c.t.Fatal(err)
	}
	slowRelay(c.t, c.layout.HostSocket(started.Box), filepath.Join(c.runtime, "boxes", string(started.Box)+".sock"), 64<<20)
	c.created = append(c.created, started.Box)
	c.boot(started.Box)
	return started
}

// lose destroys a box with its volume and brings a new one the way a lost box comes back: the host reports it gone, it fails,
// and the parent restarts the session.
func (c *realCrew) lose(id contract.BoxID, session contract.SessionID) {
	c.t.Helper()
	c.destroy(id)
	lost, err := c.s.store.Box(c.ctx, id)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.s.transition(c.ctx, c.room, lost, contract.BoxFailed, "the container is gone", ""); err != nil {
		c.t.Fatal(err)
	}
	if _, problem := c.s.SessionRestart(c.ctx, contract.Principal{Kind: contract.PrincipalSession, Session: c.root}, contract.SessionRestartRequest{Session: session}); problem != nil {
		c.t.Fatal(problem)
	}
	passLifecycle(c.t, c.s, c.ctx)
	c.boot(id)
}

func (c *realCrew) wait(what string, done func() bool) {
	c.t.Helper()
	for !done() {
		if c.ctx.Err() != nil {
			c.t.Fatalf("waited for %s, after %d model requests", what, c.upstream.count())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (c *realCrew) status(run contract.WorkflowRunID) contract.WorkflowStatusResponse {
	c.t.Helper()
	got, problem := c.s.WorkflowStatus(c.ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.WorkflowStatusRequest{Run: run})
	if problem != nil {
		c.t.Fatal(problem)
	}
	return got
}

func (c *realCrew) count(query string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.s.store.Read().QueryRowContext(c.ctx, query, args...).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

func (c *realCrew) doneSteps(run contract.WorkflowRunID) int {
	return c.count("SELECT COUNT(*) FROM workflow_steps WHERE run=? AND kind='agent' AND state='done'", run)
}

func (c *realCrew) inBox(id contract.BoxID, script string) string {
	c.t.Helper()
	out, err := exec.Command("podman", "exec", string(id), "bash", "-c", script).CombinedOutput()
	if err != nil {
		c.t.Fatalf("in the box, %s: %v: %s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The crew has closed when its session has, which is how a resumed run finds a finished one.
func (c *realCrew) closed(session contract.SessionID) {
	c.t.Helper()
	c.wait("the crew's close", func() bool {
		passLifecycle(c.t, c.s, c.ctx)
		return c.count("SELECT COUNT(*) FROM sessions WHERE id=? AND closed_at IS NOT NULL", session) == 1
	})
}

// One crew in one real box. Eleven workers on two profiles run in one container; the box is destroyed with three still working,
// and a new box finishes the script from the journal; an edited script reruns one step; an unknown profile costs nothing.
func TestWorkflowInARealBox(t *testing.T) {
	c := newRealCrew(t, nil)
	s, ctx, room, root, layout, upstream := c.s, c.ctx, c.room, c.root, c.layout, c.upstream
	start, wait, status, count, doneSteps, containers := c.start, c.wait, c.status, c.count, c.doneSteps, c.containers

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
	if got := c.inBox(first.Box, "pgrep -x pi | wc -l"); got != "11" {
		t.Fatalf("%s Pi processes in the box, want 11", got)
	}
	if got := containers("dyna.box"); !slices.Equal(got, []string{string(first.Box)}) {
		t.Fatalf("containers %v", got)
	}
	if count("SELECT COUNT(*) FROM sessions") != sessionsBefore+1 || count("SELECT COUNT(*) FROM boxes") != boxesBefore+1 {
		t.Fatal("workers took session or box rows")
	}

	// A worker names its file as it starts, so its transcript opens while it runs.
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	worker := func(step contract.WorkflowStep) (contract.TranscriptTurn, bool) {
		got, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: first.Session, Step: step.Key})
		if problem != nil {
			t.Fatalf("read %s: %+v", step.Name, problem)
		}
		if len(got.Turns) == 0 {
			return contract.TranscriptTurn{}, false
		}
		return got.Turns[0], true
	}
	wait("every worker's transcript", func() bool {
		for _, step := range status(first.Run).Steps {
			if _, read := worker(step); step.Kind == contract.WorkflowStepAgent && (step.Transcript == "" || !read) {
				return false
			}
		}
		return true
	})

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
	c.destroy(first.Box)
	upstream.release(unfinished...)
	before := upstream.count()
	c.lose(first.Box, first.Session)

	wait("the run's end", func() bool { return status(first.Run).Run.State != contract.WorkflowRunning })
	final := status(first.Run)
	// The replacement box's workers are filed under the run's turn too. Its first box is gone, and each still opens.
	for _, step := range final.Steps {
		if step.Kind != contract.WorkflowStepAgent || step.State != contract.WorkflowStepDone {
			continue
		}
		if turn, read := worker(step); !read || len(turn.Inputs) != 1 || !strings.Contains(turn.Inputs[0].Text, step.Name) || len(turn.Items) == 0 {
			t.Fatalf("worker %s reads %+v, %v", step.Name, turn, read)
		}
	}
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
	c.closed(first.Session)

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

func shellTool(command string) crewTool {
	input, _ := json.Marshal(map[string]string{"command": command})
	return crewTool{"mcp__shell__shell", string(input)}
}

const crewWriters = `export const meta = { name: 'writers', profiles: ['fast'], size: 'medium' }
phase('write')
const tasks = ['task-a', 'task-b', 'task-c', 'task-d', 'task-e', 'task-x', 'task-y']
const results = await parallel(tasks.map((task) => () => agent('Work on ' + task, { worktree: true, label: task })))
phase('merge')
const merged = await merge(results)
const tree = await run('git fetch -q origin ' + merged.branch + ' && git ls-tree --name-only FETCH_HEAD')
return { results, merged, tree: tree.stdout.trim().split('\n') }
`

type crewWriterResult struct {
	Results []struct {
		Step   string
		Text   string
		Branch string
		Commit string
		Files  []string
	}
	Merged struct {
		Branch    string
		Commit    string
		Merged    []string
		Conflicts []struct {
			Step   string
			Branch string
			Files  []string
		}
	}
	Tree []string
}

// Writers in a real box, each on its own branch, against a real git route. Five edit separate files and two edit the same line.
// The box is destroyed with its volume while one still works, and a new box keeps the others' work and reruns only that one;
// merge() gives one branch with a reported conflict and no throw; a resumed run does it again on its own branch.
func TestWorkflowWorktreesInARealBox(t *testing.T) {
	c := newRealCrew(t, map[string]string{
		"shared.txt":   "first\nsecond\nthird\n",
		"package.json": `{"name":"crew-test","version":"1.0.0","scripts":{"slow":"bash slow.sh"}}`,
		"slow.sh":      "echo start $(date +%s%N) >> /box/home/builds.log\nsleep 6\necho end $(date +%s%N) >> /box/home/builds.log\n",
	})
	upstream := c.upstream
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	tree := func(ref string) []string { return strings.Fields(c.git(c.origin, "ls-tree", "--name-only", ref)) }

	// Task a's shell names no folder, and task c uses Pi's own file tool: both must land in the worker's tree.
	upstream.tools = map[string][]crewTool{
		"task-a": {shellTool("pwd > a.txt")},
		"task-b": {shellTool("echo b > b.txt && git add -A && git commit -qm 'b by hand'")},
		"task-c": {{"write", `{"path":"c.txt","content":"c\n"}`}},
		"task-d": {shellTool("echo d > d.txt")},
		"task-e": {shellTool("echo e > e.txt")},
		"task-x": {shellTool("sed -i s/second/from-x/ shared.txt")},
		"task-y": {shellTool("sed -i s/second/from-y/ shared.txt")},
		"task-f": {shellTool("echo f > f.txt")},
	}
	upstream.hold("task-e")
	first := c.start(contract.Principal{Kind: contract.PrincipalSession, Session: c.root}, crewWriters, "")
	c.wait("six stored steps", func() bool { return c.doneSteps(first.Run) == 6 })
	if got := c.inBox(first.Box, "pgrep -x pi | wc -l"); got != "1" {
		t.Fatalf("%s Pi processes left, want the one that works on task-e", got)
	}
	// Each stored step's work is on the remote already, and nothing of it is in the main tree or left on disk.
	branches := strings.Fields(c.git(c.origin, "for-each-ref", "--format=%(refname:short)", "refs/heads/v2/"))
	if len(branches) != 6 {
		t.Fatalf("branches %v with six steps stored", branches)
	}
	if got := c.inBox(first.Box, "ls /box/work/owner/repo.worktrees | wc -l; cd /box/work/owner/repo && git worktree list | wc -l; ls"); !strings.HasPrefix(got, "1\n2\n") || strings.Contains(got, "a.txt") {
		t.Fatalf("the box holds %q", got)
	}

	before := upstream.count()
	upstream.release("task-e")
	c.lose(first.Box, first.Session)
	c.wait("the run's end", func() bool { return c.status(first.Run).Run.State != contract.WorkflowRunning })
	final := c.status(first.Run)
	for _, request := range upstream.since(before) {
		if request.task != "task-e" {
			t.Fatalf("the new box asked the model for %+v", request)
		}
	}
	if final.Run.State != contract.WorkflowDone || final.Run.Failed != 1 || !slices.Equal(final.Run.FailedSteps, []string{"merge conflict: task-y"}) {
		t.Fatalf("run %+v", final.Run)
	}
	var result crewWriterResult
	if err := json.Unmarshal([]byte(final.Run.Result), &result); err != nil {
		t.Fatalf("result %q: %v", final.Run.Result, err)
	}
	if len(result.Results) != 7 {
		t.Fatalf("result %s", final.Run.Result)
	}
	seen := map[string]bool{}
	for i, step := range result.Results {
		name := "abcdexy"[i : i+1]
		if !strings.HasPrefix(step.Branch, "v2/"+string(first.Session)+"-") || seen[step.Branch] || step.Text != "found task-"+name {
			t.Fatalf("step %+v", step)
		}
		seen[step.Branch] = true
		if got := c.git(c.origin, "rev-parse", "refs/heads/"+step.Branch); got != step.Commit {
			t.Fatalf("%s is at %s, the step stored %s", step.Branch, got, step.Commit)
		}
		want := name + ".txt"
		if name == "x" || name == "y" {
			want = "shared.txt"
		}
		if !slices.Equal(step.Files, []string{want}) {
			t.Fatalf("step %s changed %v, want %s", name, step.Files, want)
		}
	}
	if got := c.git(c.origin, "show", result.Results[0].Commit+":a.txt"); !strings.HasPrefix(got, "/box/work/owner/repo.worktrees/") {
		t.Fatalf("task-a's shell started in %q", got)
	}
	if got := c.git(c.origin, "log", "--format=%s", "-2", result.Results[1].Commit); !strings.Contains(got, "b by hand") {
		t.Fatalf("task-b's history %q", got)
	}

	// One branch has all the separate work and the first of the two that clash. It stays clean, and the other is reported.
	merged := result.Merged
	if merged.Branch != "v2/"+string(first.Session)+"-run" || c.git(c.origin, "rev-parse", "refs/heads/"+merged.Branch) != merged.Commit {
		t.Fatalf("merged %+v", merged)
	}
	want := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "package.json", "shared.txt", "slow.sh"}
	if got := tree(merged.Commit); !slices.Equal(got, want) {
		t.Fatalf("the merged tree holds %v", got)
	}
	if !slices.Equal(result.Tree, want) {
		t.Fatalf("the script saw %v", result.Tree)
	}
	if got := c.git(c.origin, "show", merged.Commit+":shared.txt"); got != "first\nfrom-x\nthird" {
		t.Fatalf("shared.txt %q", got)
	}
	if !slices.Equal(merged.Merged, []string{"task-a", "task-b", "task-c", "task-d", "task-e", "task-x"}) {
		t.Fatalf("merged %v", merged.Merged)
	}
	if len(merged.Conflicts) != 1 || merged.Conflicts[0].Step != "task-y" || !slices.Equal(merged.Conflicts[0].Files, []string{"shared.txt"}) || merged.Conflicts[0].Branch != result.Results[6].Branch {
		t.Fatalf("conflicts %+v", merged.Conflicts)
	}
	listed, problem := c.s.EventsList(c.ctx, owner, contract.EventsListRequest{Room: c.room, Types: []contract.EventType{"workflow.ended"}, Limit: 10})
	if problem != nil {
		t.Fatal(problem)
	}
	if ended := listed.Events[len(listed.Events)-1].Body.(contract.WorkflowEnded); ended.Failed != 1 || !slices.Equal(ended.FailedSteps, []string{"merge conflict: task-y"}) {
		t.Fatalf("end event %+v", ended)
	}
	c.closed(first.Session)

	// A resumed run is a new session in a new box. It keeps the old steps, whose branches it may read and not push, adds one
	// worker, and merges on its own branch. The conflict counts again.
	before = upstream.count()
	edited := strings.Replace(crewWriters, "'task-e', 'task-x'", "'task-e', 'task-f', 'task-x'", 1)
	second := c.start(owner, edited, first.Run)
	c.wait("the resumed run's end", func() bool { return c.status(second.Run).Run.State != contract.WorkflowRunning })
	for _, request := range upstream.since(before) {
		if request.task != "task-f" {
			t.Fatalf("the resumed run asked the model for %+v", request)
		}
	}
	resumed := c.status(second.Run)
	if resumed.Run.State != contract.WorkflowDone || resumed.Run.Failed != 1 || !slices.Equal(resumed.Run.FailedSteps, []string{"merge conflict: task-y"}) {
		t.Fatalf("run %+v", resumed.Run)
	}
	if err := json.Unmarshal([]byte(resumed.Run.Result), &result); err != nil {
		t.Fatal(err)
	}
	if result.Merged.Branch != "v2/"+string(second.Session)+"-run" {
		t.Fatalf("merged %+v", result.Merged)
	}
	if got := tree(result.Merged.Commit); !slices.Contains(got, "f.txt") || !slices.Contains(got, "a.txt") || !slices.Contains(got, "e.txt") {
		t.Fatalf("the resumed merge holds %v", got)
	}
	c.closed(second.Session)

	// Two workers build at once, and the script builds too, under one lock. A third worker reads while a build holds the lock.
	upstream.tools = map[string][]crewTool{
		"task-build1": {shellTool("npm run slow")},
		"task-build2": {shellTool("npm run slow")},
		"task-reader": {shellTool("until grep -q start /box/home/builds.log 2>/dev/null; do sleep 0.1; done; s=$(date +%s%N); git log -1 --oneline >/dev/null; grep -c first shared.txt; ls >/dev/null; npm ls >/dev/null; e=$(date +%s%N); echo read $s $e >> /box/home/builds.log")},
	}
	third := c.start(owner, `export const meta = { name: 'builds', profiles: ['fast'], size: 'medium' }
const steps = await parallel([
	() => agent('Work on task-build1', { worktree: true, label: 'build1' }),
	() => agent('Work on task-build2', { worktree: true, label: 'build2' }),
	() => run('npm run slow', { label: 'the script builds' }),
	() => agent('Work on task-reader', { label: 'reader' }),
])
return { steps, log: (await run('cat /box/home/builds.log')).stdout }
`, "")
	c.wait("the build run's end", func() bool { return c.status(third.Run).Run.State != contract.WorkflowRunning })
	built := c.status(third.Run)
	if built.Run.State != contract.WorkflowDone || built.Run.Failed != 0 {
		t.Fatalf("run %+v", built.Run)
	}
	var log struct{ Log string }
	if err := json.Unmarshal([]byte(built.Run.Result), &log); err != nil {
		t.Fatal(err)
	}
	t.Logf("build log:\n%s", log.Log)
	var starts, ends []int64
	var read [2]int64
	for line := range strings.Lines(log.Log) {
		fields := strings.Fields(line)
		stamp, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("log line %q", line)
		}
		switch fields[0] {
		case "start":
			if len(starts) != len(ends) {
				t.Fatalf("a build started inside another:\n%s", log.Log)
			}
			starts = append(starts, stamp)
		case "end":
			ends = append(ends, stamp)
		case "read":
			read[0] = stamp
			if read[1], err = strconv.ParseInt(fields[2], 10, 64); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("builds:\n%s", log.Log)
	}
	during := false
	for i := range starts {
		during = during || starts[i] <= read[0] && read[1] <= ends[i]
	}
	if !during {
		t.Fatalf("the reader did not finish inside one build's hold of the lock:\n%s", log.Log)
	}
	c.closed(third.Session)

	// A job that outlasts the shell tool's wait goes on in the background, and its end wakes the crew's session, which has no Pi
	// to hear it. The worker reads the job's file instead, and the wake must not disturb the run or start the script again.
	upstream.tools = map[string][]crewTool{
		"task-long": {shellTool("sleep 34; echo long > long.txt"), shellTool("until [ -e long.txt ]; do sleep 0.2; done; sleep 2")},
	}
	fourth := c.start(owner, `export const meta = { name: 'long', profiles: ['fast'], size: 'medium' }
const step = await agent('Work on task-long', { worktree: true, label: 'long' })
await run('sleep 3', { label: 'after the wake' })
return step
`, "")
	c.wait("the long run's end", func() bool { return c.status(fourth.Run).Run.State != contract.WorkflowRunning })
	long := c.status(fourth.Run)
	if long.Run.State != contract.WorkflowDone || long.Run.Failed != 0 || !strings.Contains(long.Run.Result, "long.txt") {
		t.Fatalf("run %+v", long.Run)
	}
	c.closed(fourth.Session)
	if got := c.count("SELECT COUNT(*) FROM turns WHERE session=?", fourth.Session); got != 1 {
		t.Fatalf("%d turns: the wake started the script again", got)
	}
}
