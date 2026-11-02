//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

// A stdio MCP server with one tool, in plain Node, as a box runs one.
const echoServer = `import { createInterface } from "node:readline";
const send = (id, result) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n");
createInterface({ input: process.stdin }).on("line", line => {
	const { id, method, params } = JSON.parse(line);
	if (method === "initialize") send(id, { protocolVersion: params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: "echo", version: "1.0.0" } });
	else if (method === "tools/list") send(id, { tools: [{ name: "echo", description: "Say it back", inputSchema: { type: "object", properties: { text: { type: "string" } } } }] });
	else if (method === "tools/call") send(id, { content: [{ type: "text", text: "echoed " + params.arguments.text }] });
	else if (id !== undefined) send(id, {});
});
`

// A real Pi package: one extension with one tool, and one skill.
const packageTool = `export default function (pi) {
	pi.registerTool({ name: "package_ping", label: "ping", description: "Answer from the package", parameters: { type: "object", properties: {} },
		execute: async () => ({ content: [{ type: "text", text: "pong from the package" }], details: {} }) });
}
`

// addonWorld serves git repositories that boxes can reach, and counts what they fetch.
type addonWorld struct {
	c       *realCrew
	root    string
	url     string
	fetches atomic.Int64
}

func newAddonWorld(c *realCrew) *addonWorld {
	w := &addonWorld{c: c, root: filepath.Join(c.t.TempDir(), "packages")}
	if err := os.MkdirAll(w.root, 0755); err != nil {
		c.t.Fatal(err)
	}
	backend := &cgi.Handler{Path: filepath.Join(c.git(w.root, "--exec-path"), "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + w.root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "git-upload-pack") {
			w.fetches.Add(1)
		}
		backend.ServeHTTP(rw, r)
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		c.t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	c.t.Cleanup(server.Close)
	w.url = fmt.Sprintf("http://host.containers.internal:%d", listener.Addr().(*net.TCPAddr).Port)
	// The engine has no such name; only a box does.
	c.s.addonSources.Git = func(ctx context.Context, args ...string) ([]byte, error) {
		for i := range args {
			args[i] = strings.Replace(args[i], "host.containers.internal", "127.0.0.1", 1)
		}
		return exec.CommandContext(ctx, "git", args...).Output()
	}
	return w
}

// publish makes a bare repository from files and returns its source for Pi.
func (w *addonWorld) publish(name string, files map[string]string) string {
	c, bare, seed := w.c, filepath.Join(w.root, "pkg", name+".git"), filepath.Join(w.root, "seed-"+name)
	if err := os.MkdirAll(filepath.Dir(bare), 0755); err != nil {
		c.t.Fatal(err)
	}
	c.git(w.root, "init", "--bare", "-b", "main", bare)
	for path, text := range files {
		full := filepath.Join(seed, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			c.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			c.t.Fatal(err)
		}
	}
	c.git(seed, "init", "-b", "main")
	c.git(seed, "add", "-A")
	c.git(seed, "commit", "-m", "start")
	c.git(seed, "push", bare, "main")
	return w.url + "/pkg/" + name + ".git"
}

func skillArchive(t *testing.T, name, description string, extra map[string]string) []byte {
	t.Helper()
	archive := skills.NewArchive()
	if err := archive.Add(skills.File, 0o600, []byte("---\nname: "+name+"\ndescription: "+description+"\n---\n\nThe body of "+name+".\n")); err != nil {
		t.Fatal(err)
	}
	for path, text := range extra {
		if err := archive.Add(path, 0o600, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// mirror does what a host sync does for the scopes.
func (w *addonWorld) mirror() {
	c := w.c
	for _, scope := range []contract.RoomID{"", c.room} {
		data, err := c.s.Addons().Bundle(scope)
		if err != nil {
			c.t.Fatal(err)
		}
		if data == nil {
			continue
		}
		path := c.layout.HostAddons(scope)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			c.t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			c.t.Fatal(err)
		}
	}
}

// boxOf starts one session's box.
func (w *addonWorld) boxOf(id contract.SessionID) contract.BoxID {
	c := w.c
	c.t.Helper()
	session, err := c.s.store.Session(c.ctx, id)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.sockets.AddBox(session.Box, id, c.room); err != nil {
		c.t.Fatal(err)
	}
	if err := os.MkdirAll(c.layout.HostSocketFolder(session.Box), 0755); err != nil {
		c.t.Fatal(err)
	}
	slowRelay(c.t, c.layout.HostSocket(session.Box), filepath.Join(c.runtime, "boxes", string(session.Box)+".sock"), 64<<20)
	c.created = append(c.created, session.Box)
	c.boot(session.Box)
	return session.Box
}

// steps reads the box agent's step lines, as seconds by step.
func (w *addonWorld) steps(id contract.BoxID) map[string]string {
	out, err := exec.Command("podman", "logs", string(id)).CombinedOutput()
	if err != nil {
		w.c.t.Fatal(err)
	}
	found := map[string]string{}
	for _, match := range regexp.MustCompile(`box agent: ([a-z -]+) took (\S+)`).FindAllStringSubmatch(string(out), -1) {
		found[match[1]] = match[2]
	}
	return found
}

func (w *addonWorld) notices(session contract.SessionID) []string {
	c := w.c
	listed, problem := c.s.EventsList(c.ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: c.room, Types: []contract.EventType{"session.notice"}, Limit: 100})
	if problem != nil {
		c.t.Fatal(problem)
	}
	var texts []string
	for _, event := range listed.Events {
		if body := event.Body.(contract.SessionNotice); body.Session == session {
			texts = append(texts, body.Text)
		}
	}
	return texts
}

// ask sends one task as a DM and waits for the model's last word on it.
func (w *addonWorld) ask(session contract.SessionID, task string) {
	c := w.c
	c.t.Helper()
	if _, problem := c.s.Post(c.ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: c.room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: session}, Text: task}); problem != nil {
		c.t.Fatal(problem)
	}
	c.wait("the turn "+task, func() bool {
		listed, problem := c.s.EventsList(c.ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: c.room, Types: []contract.EventType{"turn.ended"}, Limit: 1000})
		if problem != nil {
			c.t.Fatal(problem)
		}
		for _, event := range listed.Events {
			if ended := event.Body.(contract.TurnEnded); ended.Session == session && strings.Contains(ended.Final, "found "+task) {
				return true
			}
		}
		return false
	})
}

func toolInput(key, value string) string {
	data, _ := json.Marshal(map[string]string{key: value})
	return string(data)
}

// A room with add-ons in every scope, on real Podman with the real box agent and the Pi the image ships.
func TestAddonsInARealBox(t *testing.T) {
	repoSkill := func(name string) string {
		return "---\nname: " + name + "\ndescription: The repository holds this\n---\n\nBody.\n"
	}
	c := newRealCrew(t, map[string]string{".pi/skills/repo-skill/SKILL.md": repoSkill("repo-skill"), ".agents/skills/agents-skill/SKILL.md": repoSkill("agents-skill")})
	s, ctx, room, upstream := c.s, c.ctx, c.room, c.upstream
	c.git(c.cache, "fetch", c.origin, "+refs/heads/*:refs/heads/*")
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"addon.global": s.AddonGlobal()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	world := newAddonWorld(c)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	var worker contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND name='worker'", room).Scan(&worker); err != nil {
		t.Fatal(err)
	}

	// A box with nothing added, as the measure of what add-ons cost.
	base := world.boxOf(c.root)
	world.ask(c.root, "task-base")

	for _, save := range []contract.SkillSaveRequest{
		{Global: true, Archive: skillArchive(t, "global-skill", "Only the global scope has this", nil)},
		{Global: true, Archive: skillArchive(t, "clash", "The global clash", nil)},
		{Room: room, Archive: skillArchive(t, "room-skill", "Only the room has this", map[string]string{"echo.mjs": echoServer})},
		{Room: room, Archive: skillArchive(t, "clash", "The room clash", nil)},
	} {
		if _, problem := s.SkillSave(ctx, owner, save); problem != nil {
			t.Fatal(problem)
		}
	}
	good := world.publish("good", map[string]string{"package.json": `{"name":"good","version":"1.0.0"}`, "extensions/ping.js": packageTool, "skills/package-skill/SKILL.md": "---\nname: package-skill\ndescription: Bundled by the package\n---\n\nBody.\n"})
	boom := world.publish("boom", map[string]string{"package.json": `{"name":"boom","version":"1.0.0"}`, "extensions/boom.js": "throw new Error('this extension is broken');\n"})
	for _, source := range []string{good, boom, "npm:dyna-no-such-package@9.9.9"} {
		if _, problem := s.PackageAdd(ctx, owner, contract.PackageAddRequest{Room: room, Source: source}); problem != nil {
			t.Fatal(problem)
		}
	}
	for _, add := range []contract.MCPAddRequest{
		{Room: room, Name: "extra", Command: "node", Args: []string{"/box/pi/skills/room-skill/echo.mjs"}},
		{Room: room, Name: "broken", Command: "/no/such/server"},
	} {
		if _, problem := s.MCPAdd(ctx, owner, add); problem != nil {
			t.Fatal(problem)
		}
	}
	world.mirror()

	// A first box installs, and what failed neither stops it nor goes unsaid.
	first := world.boxOf(worker)
	upstream.mu.Lock()
	upstream.tools["task-tools"] = []crewTool{
		{"read", toolInput("path", "/box/pi/skills/dyna-extend/SKILL.md")},
		{"package_ping", `{}`},
		{"codemode", toolInput("code", `return await tools.mcp__extra__echo({ text: "hello" })`)},
	}
	upstream.mu.Unlock()
	world.ask(worker, "task-tools")
	system, messages := upstream.lastBody("task-tools")
	for _, want := range []string{
		"<name>dyna-extend</name>", "<description>Use when you need to add a skill or mcp server to dyna</description>",
		"<name>global-skill</name>", "<name>room-skill</name>", "<name>package-skill</name>", "<description>The room clash</description>",
		"<name>repo-skill</name>", "<name>agents-skill</name>",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("the prompt has no %s:\n%s", want, system)
		}
	}
	if strings.Contains(system, "The global clash") {
		t.Error("the global skill won a clash with the room")
	}
	for _, want := range []string{"## Create a skill", "mcp__extra", "pong from the package", "echoed hello"} {
		if !strings.Contains(messages, want) && !strings.Contains(system, want) {
			t.Errorf("the model never saw %q:\n%s", want, messages)
		}
	}
	world.waitNotices(worker, "broken", "npm:dyna-no-such-package@9.9.9", "this extension is broken")
	cold := world.fetches.Load()
	if cold == 0 {
		t.Fatal("the good package was not fetched")
	}
	if got := c.inBox(first, "pgrep -c -f box-agent"); got == "0" {
		t.Fatal("the box agent stopped")
	}

	// A second box gets the same thing from the host's cache. The broken items go first, as they would try again.
	if _, problem := s.MCPRemove(ctx, owner, contract.AddonRemoveRequest{Room: room, Name: "broken"}); problem != nil {
		t.Fatal(problem)
	}
	items, problem := s.PackageList(ctx, owner, contract.AddonListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, item := range items.Items {
		if !strings.Contains(item.Source, "/pkg/good") {
			if _, problem := s.PackageRemove(ctx, owner, contract.AddonRemoveRequest{Room: room, Name: item.Name}); problem != nil {
				t.Fatal(problem)
			}
		}
	}
	world.mirror()
	child, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "second", Profile: "test", Goal: "goal", Brief: "brief"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	before := world.fetches.Load()
	second := world.boxOf(child.Session)
	upstream.mu.Lock()
	upstream.tools["task-again"] = []crewTool{{"package_ping", `{}`}}
	upstream.mu.Unlock()
	world.ask(child.Session, "task-again")
	if _, messages = upstream.lastBody("task-again"); !strings.Contains(messages, "pong from the package") {
		t.Fatalf("the second box has no package tool:\n%s", messages)
	}
	if got := world.fetches.Load(); got != before {
		t.Fatalf("the second box fetched %d times", got-before)
	}
	if notices := world.notices(child.Session); len(notices) != 0 {
		t.Fatalf("second box notices: %q", notices)
	}

	// A session adds to its room at once, by the real command. Global waits for the owner.
	c.inBox(second, `mkdir -p /box/work/saved && printf -- '---\nname: saved-skill\ndescription: Saved from a box\n---\nBody.\n' > /box/work/saved/SKILL.md && dyna skill save /box/work/saved`)
	if found, err := s.Addons().Has(room, contract.AddonSkill, "saved-skill"); err != nil || !found {
		t.Fatalf("room save: %v %v", found, err)
	}
	asked := c.inBox(second, `dyna skill save /box/work/saved --global`)
	if !strings.Contains(asked, "waiting for the owner") {
		t.Fatalf("global save said %q", asked)
	}
	if found, _ := s.Addons().Has("", contract.AddonSkill, "saved-skill"); found {
		t.Fatal("a session's global save did not wait")
	}
	waiting, problem := s.OpList(ctx, owner, contract.OpListRequest{Room: room})
	if problem != nil || len(waiting.Operations) != 1 || waiting.Operations[0].Kind != "addon.global" {
		t.Fatalf("waiting %+v %v", waiting, problem)
	}
	if _, problem := s.OpDecide(ctx, owner, contract.OpDecideRequest{ID: waiting.Operations[0].ID, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
	c.wait("the approved save", func() bool {
		found, _ := s.Addons().Has("", contract.AddonSkill, "saved-skill")
		return found
	})

	baseSteps, firstSteps, secondSteps := world.steps(base), world.steps(first), world.steps(second)
	t.Logf("start cost: nothing added %v; first box %v; second box %v", baseSteps, firstSteps, secondSteps)
}

func (w *addonWorld) waitNotices(session contract.SessionID, wants ...string) {
	w.c.t.Helper()
	w.c.wait("the notices", func() bool {
		all := strings.Join(w.notices(session), "\n")
		for _, want := range wants {
			if !strings.Contains(all, want) {
				return false
			}
		}
		return true
	})
}
