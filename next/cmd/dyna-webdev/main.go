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
	"slices"
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
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/fakeharness"
	"github.com/resident-labs/dyna/web"
)

// host is the dev engine's one host, named as an owner's build machine might be, since the page shows it.
const host = "build-server"

var fixtureSession contract.SessionID

type devService struct {
	*engine.Service
	empty   bool
	renames *devRenames
} // api.Service needs HostAdd; engine/runtime adds the real one.

// devRenames keeps this run's new labels over devAccounts' invented ones.
type devRenames struct {
	mu     sync.Mutex
	labels map[contract.AccountID]string
}

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

// Account actions other than a rename are disabled in the fake-model development engine.
func (d devService) AccountsList(context.Context, contract.Principal, contract.AccountsListRequest) (contract.AccountsListResponse, *contract.Error) {
	accounts := []contract.Account{}
	if !d.empty {
		for _, a := range d.accounts(time.Now()) {
			state := contract.AccountConnected
			if a.usage.Cause == contract.UsageSignInExpired {
				state = contract.AccountSignInExpired
			}
			accounts = append(accounts, contract.Account{ID: a.usage.AccountID, Provider: a.provider, Label: a.usage.Label, State: state})
		}
	}
	return contract.AccountsListResponse{Providers: model.AccountProviders(), Accounts: accounts}, nil
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

// AccountsRename checks a label as the engine does, so the page's refusal shows here too.
func (d devService) AccountsRename(_ context.Context, _ contract.Principal, req contract.AccountsRenameRequest) (contract.AccountsRenameResponse, *contract.Error) {
	label, ok := model.CleanAccountLabel(req.Label)
	if !ok {
		return contract.AccountsRenameResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid account label"}
	}
	if d.empty || !slices.ContainsFunc(devAccounts(time.Now()), func(a devAccount) bool { return a.usage.AccountID == req.AccountID }) {
		return contract.AccountsRenameResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "account not found"}
	}
	d.renames.mu.Lock()
	defer d.renames.mu.Unlock()
	d.renames.labels[req.AccountID] = label
	return contract.AccountsRenameResponse{}, nil
}

// accounts is devAccounts with this run's renames.
func (d devService) accounts(now time.Time) []devAccount {
	accounts := devAccounts(now)
	d.renames.mu.Lock()
	defer d.renames.mu.Unlock()
	for i := range accounts {
		if label, ok := d.renames.labels[accounts[i].usage.AccountID]; ok {
			accounts[i].usage.Label = label
		}
	}
	return accounts
}

// devAccounts are invented, never a real sign-in. They show each look of Home's Usage and Needs you: a card of several
// windows, one of a single window, spent windows, a long label, a label that says its provider, an expired sign-in, and
// accounts Home leaves out: those that report nothing, rate limited or not. As in the engine, usage names its provider
// as the proxy does.
type devAccount struct {
	provider contract.AccountProvider
	usage    contract.AccountUsage
}

func devAccounts(now time.Time) []devAccount {
	window := func(kind contract.UsageWindowKind, used float64, reset time.Duration) contract.UsageWindow {
		w := contract.UsageWindow{Name: "seven_day", Kind: kind, UsedPercent: used, Seconds: 604800, ResetsAt: now.Add(reset)}
		if kind == contract.UsageWindowFiveHour {
			w.Name, w.Seconds = "five_hour", 18000
		}
		return w
	}
	model := func(name string, used float64) contract.UsageWindow {
		w := window(contract.UsageWindowWeekly, used, 76*time.Hour)
		w.Name, w.Model = name+" Weekly", name
		return w
	}
	account := func(provider contract.AccountProvider, proxy string, id contract.AccountID, label string, cause contract.UsageCause, windows ...contract.UsageWindow) devAccount {
		return devAccount{provider, contract.AccountUsage{AccountID: id, Provider: proxy, Label: label, Cause: cause, Windows: windows}}
	}
	return []devAccount{
		account(contract.ProviderKimi, "kimi", "kimi", "kimi", ""),
		account(contract.ProviderZAI, "openai-compatible-zai", "zai", "zai", ""),
		account(contract.ProviderAnthropic, "claude", "unused", "unused.seat", "", model("Fable", 0)),
		account(contract.ProviderAnthropic, "claude", "calm", "review.bot", "", window(contract.UsageWindowFiveHour, 0, 0), window(contract.UsageWindowWeekly, 60, 24*time.Hour)),
		account(contract.ProviderAnthropic, "claude", "studio", "Claude · studio", "", window(contract.UsageWindowFiveHour, 62, 2*time.Hour+10*time.Minute), window(contract.UsageWindowWeekly, 91, 76*time.Hour), model("Fable", 0), model("Opus", 35)),
		account(contract.ProviderOpenAI, "codex", "team", "chatgpt", "", window(contract.UsageWindowWeekly, 35, 100*time.Hour)),
		account(contract.ProviderAnthropic, "claude", "single", "night.shift", "", window(contract.UsageWindowWeekly, 56, 5*time.Hour+34*time.Minute)),
		account(contract.ProviderAnthropic, "claude", "spent", "pipsqueak.demo", "", window(contract.UsageWindowFiveHour, 0, 0), window(contract.UsageWindowWeekly, 100, 38*time.Hour)),
		account(contract.ProviderAnthropic, "claude", "long", "a-rather-long-shared-team-account-label-for-the-nightly-builds", "", window(contract.UsageWindowFiveHour, 100, 3*time.Hour), window(contract.UsageWindowWeekly, 40, 96*time.Hour)),
		account(contract.ProviderAnthropic, "claude", "spare", "weekend.plan", contract.UsageSignInExpired),
		account(contract.ProviderOpenAI, "codex", "busy", "shared.pool", contract.UsageRateLimited),
		account(contract.ProviderAnthropic, "claude", "lost", "side.project", contract.UsageUnreachable),
	}
}

// AccountsQuota answers with devAccounts.
func (d devService) AccountsQuota(context.Context, contract.Principal, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error) {
	response := contract.AccountsQuotaResponse{Accounts: []contract.AccountUsage{}}
	if !d.empty {
		for _, a := range d.accounts(time.Now()) {
			response.Accounts = append(response.Accounts, a.usage)
		}
	}
	return response, nil
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
	if req.Session == fixtureSession && fixtureSession != "" && req.Turn == "" && req.Before == "" && p.Kind == contract.PrincipalOwner {
		return contract.TranscriptReadResponse{Turns: fixtureTurns()}, nil
	}
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

func (d devService) SessionWork(ctx context.Context, p contract.Principal, req contract.SessionWorkRequest) (contract.SessionWorkResponse, *contract.Error) {
	response, problem := d.Service.SessionWork(ctx, p, req)
	if problem == nil && req.Session == fixtureSession && fixtureSession != "" {
		response.Total = len(fixtureTurns())
	}
	return response, problem
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
	svc.SetLayout(box.Layout{Data: data})
	a := api.New(devService{Service: svc, empty: *fixture == "empty", renames: &devRenames{labels: map[contract.AccountID]string{}}}, nil)
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
		seedRoom(ctx, svc, st, *posts, *agents)
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

func seedRoom(ctx context.Context, s *engine.Service, st *store.Store, posts, agents int) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e1 := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "web-studio", RootProfile: "fake"})
	lead, e2 := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "lead-web", Profile: "fake", Goal: "Build the room view"})
	child, e3 := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "builder-feed", Parent: lead.Session, Profile: "fake", Goal: "Build the feed", Brief: "Build the feed rows."})
	for _, e := range []*contract.Error{e1, e2, e3} {
		if e != nil {
			log.Fatal(e.Message)
		}
	}
	fixture, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "sync-review", Profile: "fake", Goal: "Add retry hints to the sync status"})
	if e != nil {
		log.Fatal(e.Message)
	}
	fixtureSession = fixture.Session
	post := func(p contract.Principal, r contract.PostRequest) {
		r.Room = room.Room
		if _, e := s.Post(ctx, p, r); e != nil {
			log.Fatal(e.Message)
		}
	}
	post(owner, contract.PostRequest{Text: "Welcome to the web studio. Please build a clear room view."})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}, contract.PostRequest{Text: "The **feed** and session tree are ready for review.\n\n- Check the layout\n- Review `inline code` and [the guide](https://example.com/guide)\n\n```typescript\nconst example = 'A long line that should stay inside its code block without making the whole page scroll sideways';\n```\n\n| Item | Description | Owner | Status |\n| --- | --- | --- | --- |\n| Responsive layout | A deliberately long description for a wide table with several columns | Web team | Ready for review |"})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.PostRequest{Text: "I am checking the attachment previews."})
	// A markdown-heavy post, so the markdown look shows in the feed and the thread.
	post(contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}, contract.PostRequest{Text: "## A heading\n\n### A smaller heading\n\nStatus for **review** with `inline code`:\n\n> A quoted note for the team.\n\n1. First step\n2. Second step\n\nhttps://example.com/a-very-long-bare-url-without-spaces-that-demonstrates-wrapping-in-a-narrow-session-thread\n\n- The thread renders markdown.\n- Links open in a new tab: [docs](https://example.com/docs)\n\n| Area | State | Note |\n| --- | --- | --- |\n| Thread | Done | Cells wrap inside the table when the text is long for a narrow screen |\n| Feed | Done | Rows keep the same look |\n\n\x60\x60\x60\nnpm run check -- --a-very-long-flag-name-that-keeps-going-without-any-spaces-at-all-to-test-wrapping\n\x60\x60\x60\n\nSanitizer check: ![chart](https://example.com/chart.png) <script>window.dynaSanitizerRan = true</script> <a href=\"https://example.com/sanitizer\">sanitizer link</a>"})
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
	// An invented three-version card exercises both superseded chips and the
	// in-view preview when a capture is unavailable.
	visualAuthor := contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}
	var original contract.EventID
	thumbnail := upload(visualAuthor, "weekly-thumbnail.png", "image/png", png.String())
	for i, summary := range []string{"Weekly report mockup.", "Renamed the chart and fixed the labels.", "Updated the report with this week's numbers."} {
		html := fmt.Sprintf(`<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>Weekly Report</title><style>
body{font:15px system-ui;margin:0;padding:24px;color:#e8e9eb;background:#0d0f13}h1{font-size:24px;margin:0 0 4px}p{color:#a3a8b4}.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}.tile,.chart{background:#151820;border:1px solid #272b34;border-radius:8px;padding:16px}.tile strong{display:block;font-size:24px}.chart{margin-top:12px}.bars{display:flex;align-items:end;gap:12px;height:150px}.bars span{flex:1;background:#91a2ee;min-width:12px}
</style></head><body><h1>Weekly Report</h1><p>Oct 25 – Oct 31, 2026 · sample data</p><div class="tiles"><div class="tile">Tasks done<strong>%d</strong>12%% increase</div><div class="tile">Open PRs<strong>7</strong>3 awaiting review</div><div class="tile">Failed runs<strong>2</strong>1 vs last week</div></div><div class="chart">Daily throughput<div class="bars"><span style="height:35%%"></span><span style="height:50%%"></span><span style="height:42%%"></span><span style="height:78%%"></span><span style="height:66%%"></span><span style="height:25%%"></span><span style="height:85%%"></span></div></div></body></html>`, 40+i)
		file := upload(visualAuthor, "weekly-report.html", "text/html", html)
		file.Visual = &contract.VisualCard{Title: "Weekly Report", Height: 900, Thumbnail: thumbnail.SHA256}
		result, problem := s.Post(ctx, visualAuthor, contract.PostRequest{Room: room.Room, Text: summary, VisualUpdate: original, Attachments: []contract.Attachment{file}})
		if problem != nil {
			log.Fatal(problem.Message)
		}
		if i == 0 {
			original = result.Event
		}
	}
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
	// Two addressed posts make the first and follow-up mention treatments easy to inspect.
	for i, text := range []string{"The feed is ready for review.", "The spacing now matches the design."} {
		_, err := st.Post(ctx, contract.Event{ID: contract.EventID(fmt.Sprintf("seed-mention-%d", i)), Room: room.Room, Time: time.Now(), From: by,
			Body: contract.PostCreated{Text: text, Home: contract.Conversation{Kind: contract.ConversationFeed}, Mentions: []contract.Principal{owner}}}, nil)
		fatal(err)
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

func (devService) AccountsImportOAuth(context.Context, contract.Principal, contract.AccountsImportOAuthRequest) (contract.AccountsImportOAuthResponse, *contract.Error) {
	return contract.AccountsImportOAuthResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable in webdev"}
}
