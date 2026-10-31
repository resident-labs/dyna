// dyna-webdev runs a local, fake-model engine for web development.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/fakeharness"
	"github.com/resident-labs/dyna/web"
)

type devService struct{ *engine.Service } // api.Service needs HostAdd; engine/runtime adds the real one.

func (devService) HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "webdev uses only the fake host"}
}

type profiles struct{}

func (profiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "fake", Thinking: contract.ThinkingOff}, []string{"fake"}, nil
}

type scripted struct{ fakeharness.Harness }

func (h *scripted) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	result := contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "Thanks. I will work on this:\n\n> " + strings.ReplaceAll(inputs[0].Text, "\n", "\n> ")}
	if strings.Contains(inputs[0].Text, "[fail]") {
		result = contract.TurnEnded{Outcome: contract.TurnFailed, Error: "Scripted development failure"}
	}
	h.Turns = []fakeharness.Turn{{Result: result}}
	// A [wait] turn runs until it is stopped, so the working state can be seen.
	if strings.Contains(inputs[0].Text, "[wait]") {
		h.Turns[0].Release = make(chan struct{})
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
	runner := &agent.Runner{Harness: &scripted{}, Sink: c}
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
	fatal(svc.ConfigureOperations(ctx, nil, 0))
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
	files := []struct{ name, media, body string }{{"preview.png", "image/png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDAT\x08\xd7c\xf8\xcf\xc0\xf0\x1f\x00\x05\x00\x01\xff\x89\x99=\x1d\x00\x00\x00\x00IEND\xaeB`\x82"}, {"card.html", "text/html", "<!doctype html><title>Review</title><h1>Feed review</h1><p>Ready to inspect.</p>"}}
	attachments := make([]contract.Attachment, len(files))
	for i, f := range files {
		req := httptest.NewRequest(http.MethodPost, "/v1/files?name="+f.name, strings.NewReader(f.body))
		req.Header.Set("Content-Type", f.media)
		rec := httptest.NewRecorder()
		s.Files(owner, room.Room).ServeHTTP(rec, req)
		fatal(json.Unmarshal(rec.Body.Bytes(), &attachments[i]))
	}
	post(owner, contract.PostRequest{Text: "Review these examples.", Attachments: attachments})
	for i := range posts {
		text := fmt.Sprintf("Long-list post %d of %d.", i+1, posts)
		if i%5 == 0 { // every fifth post is taller, so rows have different heights
			text += "\n\n- The feed measures each row.\n- The window keeps 1,000 events."
		}
		post(contract.Principal{Kind: contract.PrincipalSession, Session: []contract.SessionID{lead.Session, child.Session}[i%2]}, contract.PostRequest{Text: text}) // session posts wake no turns
	}
}
