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
	"github.com/resident-labs/dyna/harness/testutil"
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
// answers when its gate opens: first with its tools, one a request or as many as batch says, and then with text. A task
// named refuse fails as a provider refusal does.
type crewUpstream struct {
	mu       sync.Mutex
	requests []crewRequest
	gates    map[string]chan struct{}
	tools    map[string][]crewTool
	batch    map[string]int       // how many tools of a task one message carries, when more than one
	bodies   map[string][2]string // the last system prompt and messages of each task
}

var crewTask = regexp.MustCompile(`(task|refuse)-[a-z0-9]+`)

func (m *crewUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
		System   json.RawMessage `json:"system"`
	}
	_ = json.Unmarshal(data, &body)
	task := crewTask.FindString(string(body.Messages))
	m.mu.Lock()
	m.requests = append(m.requests, crewRequest{task, body.Model, r.Header.Get("X-Claude-Code-Session-Id")})
	if m.bodies == nil {
		m.bodies = map[string][2]string{}
	}
	m.bodies[task] = [2]string{string(body.System), string(body.Messages)}
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
	tools, batch := m.tools[task], max(m.batch[task], 1)
	m.mu.Unlock()
	if done := strings.Count(string(body.Messages), `"tool_result"`); done < len(tools) {
		for i, tool := range tools[done:min(done+batch, len(tools))] {
			input, _ := json.Marshal(tool.input)
			send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"toolu_%d","name":%q,"input":{}}}`, i, done+i, tool.name))
			send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, i, input))
			send("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
		}
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

func (m *crewUpstream) lastBody(task string) (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bodies[task][0], m.bodies[task][1]
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
	return []contract.BoxRepo{{Name: "owner/repo", Cache: r.cache, Branch: string(session), Base: "main"}}, nil
}

func (r crewRepos) Seed(ctx context.Context, session contract.SessionID, host contract.HostName) ([]contract.BoxRepo, string, error) {
	repos, err := r.Get(ctx, session, host, true)
	return repos, "", err
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
	upstream := &crewUpstream{gates: map[string]chan struct{}{}, tools: map[string][]crewTool{}, batch: map[string]int{}}
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
			model.Handler(proxySocket, "client-key", string(session.ID), allowed, []string{"account"}, "", nil, crew).ServeHTTP(w, r)
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
	if c.host, err = podman.New("local", c.runtime, "h", localRunner{}, t.TempDir()); err != nil {
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
		if err := os.MkdirAll(filepath.Dir(filepath.Join(seed, name)), 0755); err != nil {
			c.t.Fatal(err)
		}
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
		{HostPath: c.layout.HostCache(), BoxPath: "/box/cache"},
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
	deadline, _ := c.ctx.Deadline()
	testutil.Eventually(c.t, time.Until(deadline), what, func() bool {
		if done() {
			return true
		}
		if c.ctx.Err() != nil {
			c.t.Fatalf("waited for %s, after %d model requests", what, c.upstream.count())
		}
		return false
	})
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

func (crewRepos) Unavailable(context.Context, contract.RepoName) (map[string]bool, error) {
	return nil, nil
}
