// dyna-webdev runs a local, fake-model engine for web development.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
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
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/fakeharness"
	"github.com/resident-labs/dyna/web"
)

type devService struct{ *engine.Service } // api.Service needs HostAdd; engine/runtime adds the real one.

func (devService) HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "webdev uses only the fake host"}
}

// The fake box's jobs: the agent counts real jobs only, so after a turn whose final quotes [jobs], each turn of
// that session reports two here, as a box whose jobs keep running would.
var withJobs sync.Map

func (d devService) TurnOutput(ctx context.Context, p contract.Principal, req contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error) {
	if _, ok := withJobs.Load(p.Session); ok || strings.Contains(req.Final, "[jobs]") {
		withJobs.Store(p.Session, true)
		req.ShellJobs = 2
	}
	return d.Service.TurnOutput(ctx, p, req)
}
func (devService) ShellWatch(ctx context.Context, _ contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error) {
	out, run := make(chan contract.ShellJobOutput, 2), "npm run dev -- --host 127.0.0.1 --port 5173" // a reset, then a line every 250 ms
	out <- contract.ShellJobOutput{Job: "j1", Command: "go test ./engine/...", ExitCode: 1, Text: "--- FAIL: TestShellWatch (0.01s)\nFAIL\n", Reset: true}
	out <- contract.ShellJobOutput{Job: "j2", Command: run, Running: true, Text: "VITE v7.1.3  ready in 312 ms\n", Reset: true}
	go func() {
		defer close(out)
		for range time.Tick(250 * time.Millisecond) {
			select {
			case out <- contract.ShellJobOutput{Job: "j2", Command: run, Running: true, Text: "[vite] page reload src/App.svelte\n"}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type profiles struct{}

func (profiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "fake", Thinking: contract.ThinkingOff}, []string{"fake"}, nil
}

type scripted struct {
	fakeharness.Harness
	box *agent.Client
}

func (h *scripted) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	text := inputs[0].Text
	for _, in := range inputs { // a turn can batch inputs; one with a [marker] wins, so the marker works in any order
		if strings.Contains(in.Text, "[") {
			text = in.Text
		}
	}
	result := contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "Thanks. I will work on this:\n\n> " + strings.ReplaceAll(text, "\n", "\n> ")}
	if strings.Contains(text, "[fail]") {
		result = contract.TurnEnded{Outcome: contract.TurnFailed, Error: "Scripted development failure"}
	}
	h.Turns = []fakeharness.Turn{{Result: result}}
	// A [wait] turn runs until it is stopped, so the working state and its activity labels can be seen.
	if strings.Contains(text, "[wait]") {
		h.Turns[0].Release = make(chan struct{})
		begin := started
		started = func() {
			begin()
			go func() {
				for _, label := range []string{"read feed.ts", "ran node --test", "edited App.svelte"} {
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

func (b *boxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, fmt.Errorf("terminals are unavailable in webdev")
}

func (b *boxes) Create(ctx context.Context, spec engine.BoxSpec) (string, error) {
	session, err := b.st.Session(ctx, spec.Session)
	if err != nil {
		return "", err
	}
	if err = b.sockets.AddBox(spec.Box, spec.Session, session.Room); err != nil {
		return "", err
	}
	return "fake", b.Start(ctx, spec.Box)
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
		delete(b.cancel, id)
	}
	b.mu.Unlock()
	return nil
}
func (b *boxes) Recreate(ctx context.Context, spec engine.BoxSpec, _, _ string) error {
	return b.Start(ctx, spec.Box)
}
func (b *boxes) Remove(ctx context.Context, id contract.BoxID) error {
	_ = b.Stop(ctx, id) // Stop never fails.
	return b.sockets.RemoveBox(id)
}
func (b *boxes) run(ctx context.Context, id contract.BoxID) {
	c := agent.NewClient(filepath.Join(b.dir, "boxes", string(id)+".sock"))
	go c.Run(ctx)
	for _, err := c.Hello(ctx, "dev"); err != nil && ctx.Err() == nil; _, err = c.Hello(ctx, "dev") { // Create returns before the box is starting.
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
	grant := svc.HostexecGrant() // a "go" grant is agent class here, so the Requests panel shows both classes
	grant.Class = func(a contract.OpArgs, _ contract.Principal) contract.Class {
		if a.(contract.HostexecGrantArgs).Commands[0].Program == "go" {
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
	fatal(st.AddHost(ctx, box.Host{Name: "fake", State: box.HostReady, CPUs: 128, MemBytes: 256 * box.GiB, DiskBytes: 1024 * box.GiB}))
	if *seed {
		seedRoom(ctx, svc, *posts)
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
func seedRoom(ctx context.Context, s *engine.Service, posts int) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e1 := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "web-studio", RootProfile: "fake"})
	lead, e2 := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "lead-web", Profile: "fake", Goal: "Build the room view"})
	child, e3 := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "builder-feed", Parent: lead.Session, Profile: "fake", Goal: "Build the feed", Brief: "[fail] Build the feed rows."})
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
	post(owner, contract.PostRequest{Text: "[fail] Exercise the failed turn view", Mentions: []string{"builder-feed"}})
	post(owner, contract.PostRequest{Text: "[jobs] Start the dev server and run the engine tests", Mentions: []string{"lead-web"}})
	upload := func(name, media, body string) (a contract.Attachment) {
		req := httptest.NewRequest(http.MethodPost, "/v1/files?name="+name, strings.NewReader(body))
		req.Header.Set("Content-Type", media)
		rec := httptest.NewRecorder()
		s.Files(owner, room.Room).ServeHTTP(rec, req)
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
		upload("preview.png", "image/png", png.String()),
		upload("card.html", "text/html", "<!doctype html><title>Review</title><h1>Feed review</h1><p>Ready to inspect.</p>"),
	}})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.PostRequest{Text: "My notes from the review.",
		Attachments: []contract.Attachment{upload("notes.txt", "text/plain", "The feed rows render.\nThe previews load.\n")}})
	by := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	for _, program := range []string{"go", "npm"} {
		args := contract.HostexecGrantArgs{Room: room.Room, To: by, Hosts: []contract.HostName{"fake"}, Commands: []contract.CommandRule{{Program: program, Args: []string{"test"}, Prefix: true}}, Mode: contract.GrantModeApprove}
		if _, e := s.OpSubmit(ctx, by, contract.OpSubmitRequest{Operation: contract.Operation{Room: room.Room, Args: args}}); e != nil {
			log.Fatal(e.Message)
		}
	}
	for i := range posts {
		text := fmt.Sprintf("Long-list post %d of %d.", i+1, posts)
		if i%5 == 0 { // every fifth post is taller, so rows have different heights
			text += "\n\n- The feed measures each row.\n- The window keeps 1,000 events."
		}
		post(contract.Principal{Kind: contract.PrincipalSession, Session: []contract.SessionID{lead.Session, child.Session}[i%2]}, contract.PostRequest{Text: text}) // session posts wake no turns
	}
}
