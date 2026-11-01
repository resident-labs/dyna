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

type devService struct {
	*engine.Service
	empty bool
} // api.Service needs HostAdd; engine/runtime adds the real one.

func (devService) HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "webdev uses only " + host}
}

// Connect is read-only in webdev. Its status uses invented values for layout checks.
type devConnect struct{ empty bool }

func (d devConnect) Status(context.Context) (contract.ConnectStatusResponse, error) {
	if d.empty {
		return contract.ConnectStatusResponse{Attempt: contract.ConnectAttempt{State: contract.ConnectAttemptIdle}}, nil
	}
	return contract.ConnectStatusResponse{
		Attempt: contract.ConnectAttempt{State: contract.ConnectAttemptLinked}, Linked: true,
		Relay: "https://connect.example.test", Fingerprint: "SHA256:invented-machine-key",
	}, nil
}

func (devConnect) Start(context.Context, contract.ConnectStartRequest) error {
	return fmt.Errorf("Connect actions are disabled in webdev")
}
func (devConnect) Cancel(context.Context) error {
	return fmt.Errorf("Connect actions are disabled in webdev")
}
func (devConnect) Unlink(context.Context) error {
	return fmt.Errorf("Connect actions are disabled in webdev")
}

// Account actions are disabled in the fake-model development engine.
func (d devService) AccountsList(context.Context, contract.Principal, contract.AccountsListRequest) (contract.AccountsListResponse, *contract.Error) {
	accounts := []contract.Account{}
	if !d.empty {
		accounts = []contract.Account{{ID: "studio", Provider: "claude", Label: "Claude · studio", State: contract.AccountConnected}, {ID: "team", Provider: "codex", Label: "ChatGPT · team", State: contract.AccountConnected}, {ID: "spare", Provider: "kimi", Label: "Kimi · spare", State: contract.AccountSignInExpired}}
	}
	return contract.AccountsListResponse{Providers: []contract.AccountProviderInfo{{ID: "claude", DisplayName: "Claude", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowCallback}, {ID: "codex", DisplayName: "ChatGPT", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowDevice}, {ID: "kimi", DisplayName: "Kimi", ConnectMethod: contract.ConnectKey, ConnectFlow: contract.ConnectFlowKey}}, Accounts: accounts}, nil
}
func (devService) AccountsConnect(context.Context, contract.Principal, contract.AccountsConnectRequest) (contract.AccountsConnectResponse, *contract.Error) {
	return contract.AccountsConnectResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
func (devService) AccountsReconnect(context.Context, contract.Principal, contract.AccountsReconnectRequest) (contract.AccountsReconnectResponse, *contract.Error) {
	return contract.AccountsReconnectResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
func (devService) AccountsConnectStatus(context.Context, contract.Principal, contract.AccountsConnectStatusRequest) (contract.AccountsConnectStatusResponse, *contract.Error) {
	return contract.AccountsConnectStatusResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
func (devService) AccountsConnectCallback(context.Context, contract.Principal, contract.AccountsConnectCallbackRequest) (contract.AccountsConnectCallbackResponse, *contract.Error) {
	return contract.AccountsConnectCallbackResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
func (devService) AccountsSetKey(context.Context, contract.Principal, contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, *contract.Error) {
	return contract.AccountsSetKeyResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
func (devService) AccountsSetLimits(context.Context, contract.Principal, contract.AccountsSetLimitsRequest) (contract.AccountsSetLimitsResponse, *contract.Error) {
	return contract.AccountsSetLimitsResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
func (devService) AccountsRemove(context.Context, contract.Principal, contract.AccountsRemoveRequest) (contract.AccountsRemoveResponse, *contract.Error) {
	return contract.AccountsRemoveResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}

// AccountsQuota answers with invented accounts, never a real sign-in: one busy, one calm and one expired.
func (d devService) AccountsQuota(context.Context, contract.Principal, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error) {
	if d.empty {
		return contract.AccountsQuotaResponse{Accounts: []contract.AccountUsage{}}, nil
	}
	at := func(d time.Duration) time.Time { return time.Now().Add(d) }
	return contract.AccountsQuotaResponse{Accounts: []contract.AccountUsage{
		{AccountID: "studio", Provider: "claude", Label: "Claude · studio", Windows: []contract.UsageWindow{
			{Name: "five_hour", Kind: contract.UsageWindowFiveHour, UsedPercent: 62, Seconds: 18000, ResetsAt: at(2*time.Hour + 10*time.Minute)},
			{Name: "seven_day", Kind: contract.UsageWindowWeekly, UsedPercent: 91, Seconds: 604800, ResetsAt: at(76 * time.Hour)},
			{Name: "Fable Weekly", Kind: contract.UsageWindowWeekly, Model: "Fable", UsedPercent: 0, Seconds: 604800, ResetsAt: at(76 * time.Hour)},
			{Name: "Opus Weekly", Kind: contract.UsageWindowWeekly, Model: "Opus", UsedPercent: 35, Seconds: 604800, ResetsAt: at(76 * time.Hour)}}},
		{AccountID: "team", Provider: "codex", Label: "ChatGPT · team", Windows: []contract.UsageWindow{
			{Name: "seven_day", Kind: contract.UsageWindowWeekly, UsedPercent: 35, Seconds: 604800, ResetsAt: at(100 * time.Hour)}}},
		{AccountID: "spare", Provider: "kimi", Label: "Kimi · spare", Cause: contract.UsageSignInExpired},
	}}, nil
}

// HostList gives the dev host a busy beat (CPU near its limit, disk low) and adds a host that has stopped
// answering, so Home shows each look; engine/runtime fills the real load.
func (d devService) HostList(ctx context.Context, p contract.Principal, req contract.HostListRequest) (contract.HostListResponse, *contract.Error) {
	list, problem := d.Service.HostList(ctx, p, req)
	for i, h := range list.Hosts {
		switch h.Name {
		case contract.ControlPlane: // the load engine/runtime reports for its own machine
			list.Hosts[i] = contract.HostSummary{Name: h.Name, Reachable: true, CPUs: 16, MemBytes: 31 * box.GiB, CPUUsed: 3.1, MemUsed: 10 * box.GiB, DiskBytes: 931 * box.GiB, DiskUsed: 519 * box.GiB}
		case host:
			list.Hosts[i].Reachable, list.Hosts[i].Boxes, list.Hosts[i].CPUUsed, list.Hosts[i].MemUsed = true, 2, 118.4, 96*box.GiB
			list.Hosts[i].DiskBytes, list.Hosts[i].DiskUsed, list.Hosts[i].DiskLow = 1024*box.GiB, 1020*box.GiB, true
		}
	}
	// gpu-box stopped answering 4 min ago, so Home shows its last figures dimmed.
	list.Hosts = append(list.Hosts, contract.HostSummary{Name: "gpu-box", CPUs: 32, MemBytes: 128 * box.GiB, CPUUsed: 6.5, MemUsed: 41 * box.GiB,
		DiskBytes: 2048 * box.GiB, DiskUsed: 700 * box.GiB, LastSeen: time.Now().Add(-4 * time.Minute)})
	return list, problem
}

// A builder types [wait], [jobs], [fail] or [lost] in a post to script the fake turn. Seed posts read like owner messages,
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

// devSteps are invented, since the fake harness writes no transcript: failures, long output, and tools of every kind.
var devSteps = []struct {
	tool, target string
	ms           time.Duration
	failed       bool
	preview      string
}{
	{"read", "feed.ts", 42, false, "export function feedRows(events: Event[], home: Conversation) {\n  const rows: Row[] = [];\n  ..."},
	{"grep", "workSummary", 118, false, "src/lib/feed.ts:221\nsrc/lib/feed.test.ts:40\nsrc/lib/ui/WorkRow.svelte:13"},
	{"ls", "src/lib/ui", 21, false, ""},
	{"read", "docs/spec.md", 64, false, "## 5.13 Web design\nFold agent work into one muted line that expands in place."},
	{"web_fetch", "https://example.com/docs/virtual-list", 1840, false, "Virtual lists measure each row after it renders."},
	{"edit", "WorkRow.svelte", 310, false, "Replaced 4 lines with 9 lines."},
	{"bash", "node --test src/lib/feed.test.ts", 2360, true, "# Subtest: a work row names its turns\nnot ok 1 - a work row names its turns\n  expected: '2 turns'\n  actual:   '1 turn'\n# tests 41\n# pass 40\n# fail 1"},
	{"edit", "feed.ts", 280, false, "Replaced 2 lines with 3 lines."},
	{"bash", "node --test src/lib/feed.test.ts", 2110, false, "# tests 41\n# pass 41\n# fail 0"},
	{"grep", "shortPaths\\(", 96, false, "src/lib/feed.ts:208"},
	{"write", "src/lib/steps.ts", 190, false, "Wrote 38 lines."},
	{"read", "app.css", 38, false, ""},
	{"browser_snapshot", "http://127.0.0.1:5173/dm", 1250, false, "Saved a snapshot of the page."},
	{"bash", "npx svelte-check --threshold error --fail-on-warnings --tsconfig ./tsconfig.json", 96400, false, "svelte-check found 0 errors and 0 warnings"},
}

// TranscriptRead keeps the real checks, then answers with devSteps. Each turn picks a run of them from its ID, so turns
// differ in length, and the longest say that their record is cut.
func (d devService) TranscriptRead(ctx context.Context, p contract.Principal, req contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error) {
	response, problem := d.Service.TranscriptRead(ctx, p, req)
	if problem != nil {
		return response, problem
	}
	sum := crc32.ChecksumIEEE([]byte(req.Turn))
	at := time.Now().Add(-3 * time.Minute)
	for _, step := range devSteps[sum%4*3:] {
		end := at.Add(step.ms * time.Millisecond)
		response.Steps = append(response.Steps, contract.TranscriptStep{Turn: req.Turn, Tool: step.tool, Label: step.tool + " " + step.target,
			Started: at, Ended: &end, Failed: step.failed, Preview: step.preview})
		at = end.Add(4 * time.Second)
	}
	response.Truncated = sum%4 == 0
	return response, nil
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

type profiles struct{ empty bool }

func (p profiles) List() []contract.ProfileSummary {
	// Invented models, so the session profile menu has choices. Model still runs every profile on the fake harness.
	list := []contract.ProfileSummary{{Name: "fake", Provider: contract.ModelProviderAnthropic, Model: "claude-sonnet-5-5", Thinking: contract.ThinkingMedium},
		{Name: "deep", Provider: contract.ModelProviderAnthropic, Model: "claude-opus-5-5", Thinking: contract.ThinkingXHigh},
		{Name: "quick", Provider: contract.ModelProviderOpenAI, Model: "gpt-6.1-mini", Thinking: contract.ThinkingOff}}
	if !p.empty {
		for i := range list {
			list[i].Taste = 8
			list[i].Diagnostics = 7
			list[i].Intelligence = 9
			list[i].Cost = 6
		}
	}
	return list
}
func (profiles) Surface(contract.ProfileName) contract.AgentSurface { return contract.AgentSurfaceOff }

func (profiles) CompactionTokens(contract.ProfileName) int { return 0 }

func (profiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "fake", Thinking: contract.ThinkingOff}, []string{"fake"}, nil
}

// devLabels are the last activity labels of [wait] turns.
var devLabels = []string{"edited App.svelte", "reading docs/spec.md", "running go test ./engine/...", "reviewing #1481",
	"searching for StatusPill", "editing store.go", "running npm run build", "reading the CI log"}

type scripted struct {
	fakeharness.Harness
	box  *agent.Client
	lose func() // ends the box as a host loss would, with no container left
}

func (h *scripted) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	text := inputs[0].Text
	for _, in := range inputs { // a turn can batch inputs; one with a marker wins, so the marker works in any order
		if marked(in.Text, "[fail]") || marked(in.Text, "[jobs]") || marked(in.Text, "[wait]") || marked(in.Text, "[lost]") {
			text = in.Text
		}
	}
	result := contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "Thanks. I will work on this:\n\n> " + strings.ReplaceAll(text, "\n", "\n> ")}
	if marked(text, "[fail]") {
		result = contract.TurnEnded{Outcome: contract.TurnFailed, Error: "Scripted development failure", Cause: contract.CauseModelFailed}
	}
	h.Turns = []fakeharness.Turn{{Result: result}}
	// A [wait] turn runs until it is stopped, so the working state and its activity labels can be seen. A [lost] turn
	// loses its box after those labels: the turn stays open, and the reconciler marks the box lost, then failed.
	if lost := marked(text, "[lost]"); lost || marked(text, "[wait]") {
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
				if lost {
					h.lose()
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
func (b *boxes) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: host}, nil
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
func (b *boxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
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
	lose := func() {
		b.mu.Lock()
		cancel := b.cancel[id]
		delete(b.cancel, id)
		b.mu.Unlock()
		cancel()
	}
	runner := &agent.Runner{Harness: &scripted{box: c, lose: lose}, Sink: c}
	if err := c.Channel(ctx, func(m contract.BoxMessage) error { return runner.Handle(ctx, m) }, func(bool) {}); err != nil && ctx.Err() == nil {
		log.Printf("box channel: %v", err)
	}
}
func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

type routeList []string

func (r *routeList) String() string { return strings.Join(*r, ",") }
func (r *routeList) Set(route string) error {
	*r = append(*r, route)
	return nil
}

func main() {
	listen := flag.String("web.listen", "127.0.0.1:7790", "web listen address")
	seed := flag.Bool("seed", false, "create example room")
	fixture := flag.String("fixture", "", "fixture: full or empty")
	posts := flag.Int("posts", 0, "with -seed, add this many posts across the sessions for long-list checks")
	agents := flag.Int("agents", 0, "with -seed, add this many sessions across six more rooms for crowded Home checks")
	var routes routeList
	flag.Var(&routes, "web.route", "public web origin to offer when pairing, repeatable")
	flag.Parse()
	if *fixture != "" && *fixture != "full" && *fixture != "empty" {
		fmt.Fprintln(os.Stderr, "fixture must be full or empty")
		os.Exit(2)
	}
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
	svc := engine.New(st, b, profiles{empty: *fixture == "empty"}, time.Now)
	fatal(svc.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": svc.HostexecGrant()}, time.Hour))
	svc.SetFilesDir(filepath.Join(data, "files"))
	svc.SetTranscriptDir(filepath.Join(data, "transcripts"))
	a := api.New(devService{Service: svc, empty: *fixture == "empty"}, nil)
	a.SetConnectOperations(devConnect{empty: *fixture == "empty"})
	a.SetFilesRoute(func(c api.Caller) http.Handler { return svc.Files(c.Principal, c.Room) })
	app, err := fs.Sub(web.Dist, "dist")
	fatal(err)
	webServer, err := api.NewWeb(a, *listen, app, time.Now, st)
	fatal(err)
	fatal(webServer.SetRoutes(routes))
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
	if *seed || *fixture == "full" {
		seedRoom(ctx, svc, *posts, *agents)
		seedDevices(ctx, st)
	} else if *fixture == "empty" {
		_, problem := svc.RoomCreate(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomCreateRequest{Name: "web-studio", RootProfile: "fake"})
		if problem != nil {
			fatal(fmt.Errorf("create empty fixture room: %s", problem.Message))
		}
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

// seedDevices adds invented devices, so Settings shows a full list and an expiry warning.
func seedDevices(ctx context.Context, st *store.Store) {
	now := time.Now()
	for _, d := range []store.Device{
		{ID: "device_phone", Name: "iPhone", TokenHash: [32]byte{1}, PairedAt: now.Add(-9 * 24 * time.Hour), LastSeenAt: now.Add(-12 * time.Minute), ExpiresAt: now.Add(store.DeviceLife),
			Scopes: []contract.Scope{contract.ScopeRead, contract.ScopeOperate, contract.ScopeFiles, contract.ScopeTerminal}},
		{ID: "device_laptop", Name: "Work laptop", TokenHash: [32]byte{2}, PairedAt: now.Add(-40 * 24 * time.Hour), LastSeenAt: now.Add(-28 * 24 * time.Hour), ExpiresAt: now.Add(2 * 24 * time.Hour),
			Scopes: []contract.Scope{contract.ScopeRead, contract.ScopeOperate}},
	} {
		fatal(st.CreateDevice(ctx, d))
	}
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
		case i%17 == 11:
			brief = "[lost]"
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
	post(contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}, contract.PostRequest{Text: "The **feed** and session tree are ready for review.\n\n- Check the layout\n- Review `inline code` and [the guide](https://example.com/guide)\n\n```typescript\nconst example = 'A long line that should stay inside its code block without making the whole page scroll sideways';\n```\n\n| Item | Description | Owner | Status |\n| --- | --- | --- | --- |\n| Responsive layout | A deliberately long description for a wide table with several columns | Web team | Ready for review |"})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.PostRequest{Text: "I am checking the attachment previews."})
	post(owner, contract.PostRequest{Text: "What is the next step?", Home: contract.Conversation{Kind: contract.ConversationDM, Session: lead.Session}})
	post(owner, contract.PostRequest{Text: "Exercise the failed turn view", Mentions: []string{"builder-feed"}})
	post(owner, contract.PostRequest{Text: "Start the dev server and run the engine tests", Mentions: []string{"lead-web"}})
	post(owner, contract.PostRequest{Text: "Then tidy the room menu", Mentions: []string{"builder-feed"}})
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
