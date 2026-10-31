// dyna-webdev runs a local, fake-model engine for web development.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	pngenc "image/png"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/fakeharness"
	"github.com/resident-labs/dyna/web"
)

// host is the dev engine's one host, named as an owner's build machine might be, since the page shows it.
const host = "build-server"

type devService struct{ *engine.Service } // api.Service needs HostAdd; engine/runtime adds the real one.

func (devService) HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "webdev uses only " + host}
}

// AccountsQuota answers with invented accounts, never a real sign-in: one busy, one calm and one expired.
func (devService) AccountsQuota(context.Context, contract.Principal, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error) {
	at := func(d time.Duration) time.Time { return time.Now().Add(d) }
	return contract.AccountsQuotaResponse{Accounts: []contract.AccountUsage{
		{Provider: "claude", Label: "Claude · studio", Windows: []contract.UsageWindow{
			{Name: "five_hour", UsedPercent: 62, Seconds: 18000, ResetsAt: at(2*time.Hour + 10*time.Minute)},
			{Name: "seven_day", UsedPercent: 91, Seconds: 604800, ResetsAt: at(76 * time.Hour)}}},
		{Provider: "codex", Label: "ChatGPT · team", Windows: []contract.UsageWindow{
			{Name: "seven_day", UsedPercent: 35, Seconds: 604800, ResetsAt: at(100 * time.Hour)}}},
		{Provider: "kimi", Label: "Kimi · spare", Cause: contract.UsageSignInExpired},
	}}, nil
}

// HostList gives the dev host a busy beat (CPU and disk near their limits) and adds a host that has stopped
// answering, so Home shows each look; engine/runtime fills the real load.
func (d devService) HostList(ctx context.Context, p contract.Principal, req contract.HostListRequest) (contract.HostListResponse, *contract.Error) {
	list, problem := d.Service.HostList(ctx, p, req)
	for i, h := range list.Hosts {
		switch h.Name {
		case contract.ControlPlane: // the load engine/runtime reports for its own machine
			list.Hosts[i] = contract.HostSummary{Name: h.Name, Reachable: true, CPUs: 16, MemBytes: 31 * box.GiB, CPUUsed: 3.1, MemUsed: 10 * box.GiB, DiskBytes: 931 * box.GiB, DiskUsed: 519 * box.GiB}
		case host:
			list.Hosts[i].Reachable, list.Hosts[i].Boxes, list.Hosts[i].CPUUsed, list.Hosts[i].MemUsed = true, 2, 118.4, 96*box.GiB
			list.Hosts[i].DiskBytes, list.Hosts[i].DiskUsed = 1024*box.GiB, 960*box.GiB
		}
	}
	// gpu-box stopped answering 4 min ago, so Home shows its last figures dimmed.
	list.Hosts = append(list.Hosts, contract.HostSummary{Name: "gpu-box", CPUs: 32, MemBytes: 128 * box.GiB, CPUUsed: 6.5, MemUsed: 41 * box.GiB,
		DiskBytes: 2048 * box.GiB, DiskUsed: 700 * box.GiB, LastSeen: time.Now().Add(-4 * time.Minute)})
	return list, problem
}

// A builder types [wait], [jobs] or [fail] in a post to script the fake turn. Seed posts read like owner messages,
// so each seed text acts as its marker instead.
var seeded = map[string]string{
	"Build the feed rows.":                          "[fail]",
	"Exercise the failed turn view":                 "[fail]",
	"Start the dev server and run the engine tests": "[jobs]",
	"Then tidy the room menu":                       "[wait]",
}

func marked(text, marker string) bool {
	for seed, m := range seeded {
		if m == marker && strings.Contains(text, seed) {
			return true
		}
	}
	return strings.Contains(text, marker)
}

// The fake box's jobs: the agent counts real jobs only, so after a turn whose final quotes a [jobs] post, each turn of
// that session reports two here, as a box whose jobs keep running would.
var withJobs sync.Map

func (d devService) TurnOutput(ctx context.Context, p contract.Principal, req contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error) {
	if _, ok := withJobs.Load(p.Session); ok || marked(req.Final, "[jobs]") {
		if _, seen := withJobs.LoadOrStore(p.Session, true); !seen {
			d.Service.ShellJobEnded(ctx, p, contract.ShellJobEndedRequest{Job: "j1", ExitCode: 1, Tail: "FAIL"})
		}
		req.ShellJobs = 1 // ShellWatch has one running job
	}
	return d.Service.TurnOutput(ctx, p, req)
}
func (devService) ShellWatch(ctx context.Context, _ contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error) {
	out, run := make(chan contract.ShellJobOutput, 4), "npm run dev -- --host 127.0.0.1 --port 5173" // a reset, then a line every 250 ms
	now := time.Now()
	out <- contract.ShellJobOutput{Job: "j0", Command: "npm run build", Text: "built in 1.35s\n", Reset: true,
		Started: now.Add(-2 * time.Hour), Ended: now.Add(-2*time.Hour + 12*time.Second)}
	out <- contract.ShellJobOutput{Job: "j3", Command: "go vet ./...", Text: "", Reset: true,
		Started: now.Add(-5 * time.Minute), Ended: now.Add(-5*time.Minute + 38*time.Second)}
	out <- contract.ShellJobOutput{Job: "j1", Command: "go test ./engine/...", ExitCode: 1, Text: "--- FAIL: TestShellWatch (0.01s)\nFAIL\n", Reset: true,
		Started: now.Add(-3 * time.Minute), Ended: now.Add(-3*time.Minute + 38*time.Second)}
	out <- contract.ShellJobOutput{Job: "j2", Command: run, Running: true, Text: "VITE v7.1.3  ready in 312 ms\n", Reset: true, Started: now.Add(-2 * time.Minute)}
	go func() {
		defer close(out)
		for range time.Tick(250 * time.Millisecond) {
			select {
			case out <- contract.ShellJobOutput{Job: "j2", Command: run, Running: true, Text: "[vite] page reload src/App.svelte\n", Started: now.Add(-2 * time.Minute)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// SessionList shows builder-feed's computer as running, so the computer view can be opened.
func (d devService) SessionList(ctx context.Context, p contract.Principal, req contract.SessionListRequest) (contract.SessionListResponse, *contract.Error) {
	list, problem := d.Service.SessionList(ctx, p, req)
	for i := range list.Sessions {
		if list.Sessions[i].Name == "builder-feed" {
			list.Sessions[i].Computer = contract.ComputerRunning
		}
	}
	return list, problem
}

// ComputerWatch streams a 16:10 desktop with a bar that moves each frame, twice a second. The first frame comes after
// 4 s, as after an engine restart, so the Connecting state can be seen.
func (devService) ComputerWatch(ctx context.Context, _ contract.SessionID) (<-chan []byte, *contract.Error) {
	frames := make(chan []byte)
	go func() {
		defer close(frames)
		select {
		case <-time.After(4 * time.Second):
		case <-ctx.Done():
			return
		}
		for i := 0; ; i++ {
			shot := image.NewRGBA(image.Rect(0, 0, 1280, 800))
			for y := range 800 {
				for x := range 1280 {
					c := color.RGBA{uint8(30 + y/10), uint8(50 + x/16), 110, 255}
					if y > 120 && y < 640 && x > 200 && x < 1080 { // a window, with a bar that moves each frame
						c = color.RGBA{236, 233, 228, 255}
						if y < 160 || (y > 300 && y < 330 && x < 220+(i*40)%840) {
							c = color.RGBA{70, 90, 160, 255}
						}
					}
					shot.Set(x, y, c)
				}
			}
			var frame bytes.Buffer
			fatal(jpeg.Encode(&frame, shot, nil))
			select {
			case frames <- frame.Bytes():
			case <-ctx.Done():
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	return frames, nil
}

type profiles struct{}

func (profiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "fake", Thinking: contract.ThinkingOff}, []string{"fake"}, nil
}

// devLabels are the last activity labels of [wait] turns.
var devLabels = []string{"edited App.svelte", "reading docs/spec.md", "running go test ./engine/...", "reviewing #1481",
	"searching for StatusPill", "editing store.go", "running npm run build", "reading the CI log"}

type scripted struct {
	fakeharness.Harness
	box *agent.Client
}

func (h *scripted) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	text := inputs[0].Text
	for _, in := range inputs { // rule notices ride along with the next turn, so the reply quotes the first message instead
		if !strings.HasPrefix(in.Text, "Rule r") {
			text = in.Text
			break
		}
	}
	for _, in := range inputs { // a turn can batch inputs; one with a marker wins, so the marker works in any order
		if marked(in.Text, "[fail]") || marked(in.Text, "[jobs]") || marked(in.Text, "[wait]") {
			text = in.Text
		}
	}
	result := contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "Thanks. I will work on this:\n\n> " + strings.ReplaceAll(text, "\n", "\n> ")}
	if marked(text, "[fail]") {
		result = contract.TurnEnded{Outcome: contract.TurnFailed, Error: "Scripted development failure", Cause: contract.CauseModelFailed}
	}
	h.Turns = []fakeharness.Turn{{Result: result}}
	// A [wait] turn runs until it is stopped, so the working state and its activity labels can be seen.
	if marked(text, "[wait]") {
		h.Turns[0].Release = make(chan struct{})
		begin := started
		started = func() {
			begin()
			go func() {
				// Each turn ends on its own label, picked from its ID, so a crowded Home shows varied work.
				last := devLabels[int(crc32.ChecksumIEEE([]byte(id))%uint32(len(devLabels)))]
				for _, label := range []string{"read feed.ts", "ran node --test", last} {
					_ = h.box.TurnActivity(ctx, contract.TurnActivityRequest{Turn: id, Label: label}) // A lost label only leaves the last one showing.
					time.Sleep(2 * time.Second)
				}
			}()
		}
	}
	return h.Harness.Start(ctx, id, inputs, started)
}

type boxes struct {
	ctx     context.Context
	st      *store.Store
	sockets *api.Sockets
	dir     string
	mu      sync.Mutex
	cancel  map[contract.BoxID]context.CancelFunc
}

// Every webdev terminal is a plain local shell, so the web app's terminal can be tried and checked.
func (b *boxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return []string{"bash", "--norc", "--noprofile"}, nil
}

func (b *boxes) Create(ctx context.Context, spec lifecycle.BoxSpec) (string, error) {
	session, err := b.st.Session(ctx, spec.Session)
	if err != nil {
		return "", err
	}
	if err = b.sockets.AddBox(spec.Box, spec.Session, session.Room); err != nil {
		return "", err
	}
	return host, b.Start(ctx, spec.Box)
}
func (b *boxes) Inspect(_ context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cancel, present := b.cancel[id]
	return box.ContainerStatus{Box: id, Running: cancel != nil}, present, nil
}
func (b *boxes) Start(_ context.Context, id contract.BoxID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel[id] != nil {
		return nil
	}
	boxCtx, cancel := context.WithCancel(b.ctx)
	b.cancel[id] = cancel
	go b.run(boxCtx, id)
	return nil
}
func (b *boxes) Stop(_ context.Context, id contract.BoxID) error {
	b.mu.Lock()
	if cancel := b.cancel[id]; cancel != nil {
		cancel()
		b.cancel[id] = nil
	}
	b.mu.Unlock()
	return nil
}
func (b *boxes) Recreate(ctx context.Context, spec lifecycle.BoxSpec, _, _ string) error {
	return b.Start(ctx, spec.Box)
}
func (b *boxes) Remove(ctx context.Context, id contract.BoxID) error {
	_ = b.Stop(ctx, id) // Stop never fails.
	b.mu.Lock()
	delete(b.cancel, id)
	b.mu.Unlock()
	return b.sockets.RemoveBox(id)
}
func (b *boxes) run(ctx context.Context, id contract.BoxID) {
	c := agent.NewClient(filepath.Join(b.dir, "boxes", string(id)+".sock"))
	go c.Run(ctx)
	for _, err := c.Hello(ctx); err != nil && ctx.Err() == nil; _, err = c.Hello(ctx) { // Create returns before the box is starting.
		time.Sleep(50 * time.Millisecond)
	}
	runner := &agent.Runner{Harness: &scripted{box: c}, Sink: c}
	if err := c.Channel(ctx, func(m contract.BoxMessage) error { return runner.Handle(ctx, m) }, func(bool) {}); err != nil && ctx.Err() == nil {
		log.Printf("box channel: %v", err)
	}
}
func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func main() {
	listen := flag.String("web.listen", "127.0.0.1:7790", "web listen address")
	seed := flag.Bool("seed", false, "create example room")
	posts := flag.Int("posts", 0, "with -seed, add this many posts across the sessions for long-list checks")
	agents := flag.Int("agents", 0, "with -seed, add this many sessions across six more rooms for crowded Home checks")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	data, err := os.MkdirTemp("", "dyna-webdev-")
	fatal(err)
	defer os.RemoveAll(data)
	st, err := store.Open(data)
	fatal(err)
	defer st.Close()
	runtime := filepath.Join(data, "runtime")
	b := &boxes{ctx: ctx, st: st, dir: runtime, cancel: map[contract.BoxID]context.CancelFunc{}}
	svc := engine.New(st, b, profiles{}, time.Now)
	grant := svc.HostexecGrant() // a test grant is agent class here, so the Requests panel shows both classes
	grant.Class = func(a contract.OpArgs, _ contract.Principal) contract.Class {
		if a.(contract.HostexecGrantArgs).Reason == "Run Go tests on the host" {
			return contract.ClassAgent
		}
		return contract.ClassOwner
	}
	fatal(svc.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": grant}, time.Hour))
	svc.SetFilesDir(filepath.Join(data, "files"))
	svc.SetTranscriptDir(filepath.Join(data, "transcripts"))
	a := api.New(devService{svc}, nil)
	a.SetFilesRoute(func(c api.Caller) http.Handler { return svc.Files(c.Principal, c.Room) })
	app, err := fs.Sub(web.Dist, "dist")
	fatal(err)
	webServer, err := api.NewWeb(a, *listen, app, time.Now)
	fatal(err)
	listener, err := webServer.Listen()
	fatal(err)
	defer listener.Close()
	b.sockets = api.NewSockets(a, runtime, uint32(os.Getuid()), nil)
	fatal(b.sockets.OpenOwner())
	defer b.sockets.Close()
	// The engine's reconciler starts and stops boxes; each session's box waits as pending until a pass takes it.
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	reconciled := make(chan struct{})
	go func() {
		defer close(reconciled)
		svc.Reconciler().Run(ctx, ticks.C)
	}()
	defer func() {
		<-reconciled
		svc.Reconciler().Wait()
	}()
	fatal(st.AddHost(ctx, box.Host{Name: host, State: box.HostReady, CPUs: 128, MemBytes: 256 * box.GiB, DiskBytes: 1024 * box.GiB}))
	if *seed {
		seedRoom(ctx, svc, *posts, *agents)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtime, "owner.sock"))
	}}
	resp, err := (&http.Client{Transport: transport}).Post("http://dyna/v1/web.login", "application/json", strings.NewReader("{}"))
	fatal(err)
	defer resp.Body.Close()
	var login contract.WebLoginResponse
	fatal(json.NewDecoder(resp.Body).Decode(&login))
	fmt.Println(login.URL)
	<-ctx.Done()
}

// seedAgents spreads n sessions with invented names over six more rooms: every third one works until stopped,
// every seventeenth fails, and the rest are idle, so Home's room activity has a crowd to sum.
func seedAgents(ctx context.Context, s *engine.Service, n int) {
	if n == 0 {
		return
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	names := []string{"engine-core", "docs-site", "mobile-app", "data-pipeline", "billing", "design-system"}
	roles := []string{"builder", "reviewer", "tester", "writer", "scout"}
	rooms := make([]contract.RoomID, len(names))
	for i, name := range names {
		room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: name, RootProfile: "fake"})
		if e != nil {
			log.Fatal(e.Message)
		}
		rooms[i] = room.Room
	}
	for i := range n {
		brief := ""
		switch {
		case i%17 == 5:
			brief = "[fail]"
		case i%3 == 0:
			brief = "[wait]"
		}
		// Rooms fill unevenly (the first takes a third), so the busiest-first order shows.
		room := rooms[[]int{0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 5}[i%21]]
		name := fmt.Sprintf("%s-%d", roles[i%len(roles)], i+1)
		if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "fake", Goal: "Help the room", Brief: brief}); e != nil {
			log.Fatal(e.Message)
		}
	}
}

func seedRoom(ctx context.Context, s *engine.Service, posts, agents int) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e1 := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "web-studio", RootProfile: "fake"})
	lead, e2 := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "lead-web", Profile: "fake", Goal: "Build the room view"})
	child, e3 := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "builder-feed", Parent: lead.Session, Profile: "fake", Goal: "Build the feed", Brief: "Build the feed rows."})
	for _, e := range []*contract.Error{e1, e2, e3} {
		if e != nil {
			log.Fatal(e.Message)
		}
	}
	post := func(p contract.Principal, r contract.PostRequest) {
		r.Room = room.Room
		if _, e := s.Post(ctx, p, r); e != nil {
			log.Fatal(e.Message)
		}
	}
	post(owner, contract.PostRequest{Text: "Welcome to the web studio. Please build a clear room view."})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}, contract.PostRequest{Text: "The feed and session tree are ready for review."})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.PostRequest{Text: "I am checking the attachment previews."})
	post(owner, contract.PostRequest{Text: "What is the next step?", Home: contract.Conversation{Kind: contract.ConversationDM, Session: lead.Session}})
	post(owner, contract.PostRequest{Text: "Exercise the failed turn view", Mentions: []string{"builder-feed"}})
	post(owner, contract.PostRequest{Text: "Start the dev server and run the engine tests", Mentions: []string{"lead-web"}})
	post(owner, contract.PostRequest{Text: "Then tidy the room menu", Mentions: []string{"builder-feed"}})
	// Room rules from the owner and from the root session, the only session that may add them.
	listed, e := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room.Room})
	if e != nil {
		log.Fatal(e.Message)
	}
	root := owner
	for _, item := range listed.Sessions {
		if item.Parent == "" {
			root = contract.Principal{Kind: contract.PrincipalSession, Session: item.ID}
		}
	}
	for _, rule := range []struct {
		by   contract.Principal
		text string
	}{
		{owner, "Tests must be fast and high-signal: one fail-first test per fix, under 1 second, with no sleeps."},
		{root, "Never poll GitHub or Dyna in shell loops; wait for the engine's notices."},
		{owner, "Builds and test runs go to hermeslab, never to the desktop node."},
		{owner, "Every UI change must be an obvious, tasteful improvement over v1's matching screen. Builders screenshot v1's screen and a strong product's piece first, and each UI change shows them beside its own."},
		{root, "Each reviewer reads spec.md first and never reopens settled owner decisions."},
	} {
		if _, e := s.RuleAdd(ctx, rule.by, contract.RuleAddRequest{Room: room.Room, Text: rule.text}); e != nil {
			log.Fatal(e.Message)
		}
	}
	// A session may attach only files uploaded in its room, so each file is uploaded by the principal that posts it.
	upload := func(by contract.Principal, name, media, body string) (a contract.Attachment) {
		req := httptest.NewRequest(http.MethodPost, "/v1/files?name="+name, strings.NewReader(body))
		req.Header.Set("Content-Type", media)
		rec := httptest.NewRecorder()
		s.Files(by, room.Room).ServeHTTP(rec, req)
		fatal(json.Unmarshal(rec.Body.Bytes(), &a))
		return a
	}
	// A 640x360 gradient, so the image thumbnail has something to show.
	shot := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := range 360 {
		for x := range 640 {
			shot.Set(x, y, color.RGBA{uint8(40 + x/5), uint8(60 + y/3), 150, 255})
		}
	}
	var png bytes.Buffer
	fatal(pngenc.Encode(&png, shot))
	post(owner, contract.PostRequest{Text: "Review these examples.", Attachments: []contract.Attachment{
		upload(owner, "preview.png", "image/png", png.String()),
		upload(owner, "card.html", "text/html", "<!doctype html><title>Review</title><h1>Feed review</h1><p>Ready to inspect.</p>"),
	}})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.PostRequest{Text: "My notes from the review.",
		Attachments: []contract.Attachment{upload(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, "notes.txt", "text/plain", "The feed rows render.\nThe previews load.\n")}})
	by := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	for _, reason := range []string{"Run Go tests on the host", "Inspect the host service logs"} {
		args := contract.HostexecGrantArgs{Room: room.Room, To: by, Hosts: []contract.HostName{host}, Reason: reason}
		if _, e := s.OpSubmit(ctx, by, contract.OpSubmitRequest{Operation: contract.Operation{Room: room.Room, Args: args}}); e != nil {
			log.Fatal(e.Message)
		}
	}
	seedAgents(ctx, s, agents)
	for i := range posts {
		text := fmt.Sprintf("Long-list post %d of %d.", i+1, posts)
		if i%5 == 0 { // every fifth post is taller, so rows have different heights
			text += "\n\n- The feed measures each row.\n- The window keeps 1,000 events."
		}
		post(contract.Principal{Kind: contract.PrincipalSession, Session: []contract.SessionID{lead.Session, child.Session}[i%2]}, contract.PostRequest{Text: text}) // session posts wake no turns
	}
}
