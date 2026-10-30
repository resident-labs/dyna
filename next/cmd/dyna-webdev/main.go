// dyna-webdev runs a local, fake-model engine for web development.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/fakeharness"
	"github.com/resident-labs/dyna/web"
)

type devService struct{ *engine.Service }

func (s devService) HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "webdev uses only the fake host"}
}

type profiles struct{}

func (profiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "fake", Thinking: contract.ThinkingOff}, []string{"fake"}, nil
}

type scripted struct{ fakeharness.Harness }

func (h *scripted) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	text := inputs[0].Text
	result := contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "Thanks. I will work on this:\n\n> " + strings.ReplaceAll(text, "\n", "\n> ")}
	if strings.Contains(text, "[fail]") {
		result.Outcome = contract.TurnFailed
		result.Final = ""
		result.Error = "Scripted development failure"
	}
	h.Turns = []fakeharness.Turn{{Result: result}}
	return h.Harness.Start(ctx, id, inputs, started)
}

type boxes struct {
	ctx     context.Context
	st      *store.Store
	sockets *api.Sockets
	dir     string
	host    *fakehost.Host
	mu      sync.Mutex
}

func (b *boxes) Create(ctx context.Context, spec engine.BoxSpec) (string, error) {
	session, err := b.st.Session(ctx, spec.Session)
	if err != nil {
		return "", err
	}
	if err = b.sockets.AddBox(spec.Box, spec.Session, session.Room); err != nil {
		return "", err
	}
	b.mu.Lock()
	err = b.host.CreateContainer(ctx, box.ContainerSpec{Box: spec.Box, Image: "fake", Slice: spec.Slice})
	if err == nil {
		err = b.host.StartContainer(ctx, spec.Box)
	}
	b.mu.Unlock()
	if err != nil {
		return "", err
	}
	go b.run(spec.Box)
	return "fake", nil
}
func (b *boxes) Remove(ctx context.Context, id contract.BoxID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.host.RemoveContainer(ctx, id); err != nil {
		return err
	}
	return b.sockets.RemoveBox(id)
}
func (b *boxes) run(id contract.BoxID) {
	c := agent.NewClient(filepath.Join(b.dir, "boxes", string(id)+".sock"))
	go c.Run(b.ctx)
	// Creation returns before the engine moves the box to starting.
	for {
		if _, err := c.Hello(b.ctx, "dev"); err == nil {
			break
		}
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	runner := &agent.Runner{Harness: &scripted{}, Sink: c}
	if err := c.Channel(b.ctx, func(m contract.BoxMessage) error { return runner.Handle(b.ctx, m) }); err != nil && b.ctx.Err() == nil {
		log.Printf("box channel: %v", err)
	}
}
func check(e *contract.Error) {
	if e != nil {
		log.Fatal(e.Message)
	}
}
func main() {
	data := flag.String("data", "", "data directory (default: temporary)")
	listen := flag.String("web.listen", "127.0.0.1:7790", "web listen address")
	seed := flag.Bool("seed", false, "create example room")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *data == "" {
		var err error
		*data, err = os.MkdirTemp("", "dyna-webdev-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(*data)
	}
	st, err := store.Open(*data, time.Now)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	runtime := filepath.Join(*data, "runtime")
	b := &boxes{ctx: ctx, st: st, dir: runtime, host: fakehost.New()}
	svc := engine.New(st, b, profiles{}, time.Now)
	if err = svc.ConfigureOperations(ctx, nil, 0); err != nil {
		log.Fatal(err)
	}
	svc.SetFilesDir(filepath.Join(*data, "files"))
	svc.SetTranscriptDir(filepath.Join(*data, "transcripts"))
	a := api.New(devService{svc}, nil)
	a.SetFilesRoute(func(c api.Caller) http.Handler { return svc.Files(c.Principal, c.Room) })
	_, port, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatal(err)
	}
	app, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}
	listener, err := api.NewWeb(a, port, app, time.Now).Listen(*listen)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	b.sockets = api.NewSockets(a, runtime, uint32(os.Getuid()), nil)
	if err = b.sockets.OpenOwner(); err != nil {
		log.Fatal(err)
	}
	defer b.sockets.Close()
	hosts, err := st.Hosts(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if len(hosts) == 0 {
		if err = st.AddHost(ctx, box.Host{Name: "fake", State: box.HostReady, CPUs: 128, MemBytes: 256 * box.GiB, DiskBytes: 1024 * box.GiB}); err != nil {
			log.Fatal(err)
		}
	}
	existing, err := st.Boxes(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, current := range existing {
		if current.State != contract.BoxReady && current.State != contract.BoxStarting {
			continue
		}
		session, err := st.Session(ctx, current.Session)
		if err != nil {
			log.Fatal(err)
		}
		if err := b.sockets.AddBox(current.ID, current.Session, session.Room); err != nil {
			log.Fatal(err)
		}
		go b.run(current.ID)
	}
	if *seed {
		seedRoom(ctx, svc, *data)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtime, "owner.sock"))
	}}
	resp, err := (&http.Client{Transport: transport}).Post("http://dyna/v1/web.login", "application/json", strings.NewReader("{}"))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	var login contract.WebLoginResponse
	if err = json.NewDecoder(resp.Body).Decode(&login); err != nil {
		log.Fatal(err)
	}
	fmt.Println(login.URL)
	<-ctx.Done()
}
func seedRoom(ctx context.Context, s *engine.Service, data string) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "web-studio", RootProfile: "fake"})
	check(e)
	lead, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "lead-web", Profile: "fake", Goal: "Build the room view"})
	check(e)
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "builder-feed", Parent: lead.Session, Profile: "fake", Goal: "Build the feed", Brief: "[fail] Exercise the failed turn view"})
	check(e)
	post := func(p contract.Principal, r contract.PostRequest) {
		r.Room = room.Room
		_, e := s.Post(ctx, p, r)
		check(e)
	}
	post(owner, contract.PostRequest{Text: "Welcome to the web studio. Please build a clear room view."})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}, contract.PostRequest{Text: "The feed and session tree are ready for review."})
	post(contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.PostRequest{Text: "I am checking the attachment previews."})
	post(owner, contract.PostRequest{Text: "What is the next step?", Home: contract.Conversation{Kind: contract.ConversationDM, Session: lead.Session}})
	post(owner, contract.PostRequest{Text: "[fail] Exercise the failed turn view", Mentions: []string{"builder-feed"}})
	files := []struct{ name, media, body string }{{"preview.png", "image/png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDAT\x08\xd7c\xf8\xcf\xc0\xf0\x1f\x00\x05\x00\x01\xff\x89\x99=\x1d\x00\x00\x00\x00IEND\xaeB`\x82"}, {"card.html", "text/html", "<!doctype html><title>Review</title><h1>Feed review</h1><p>Ready to inspect.</p>"}}
	var attachments []contract.Attachment
	if err := os.MkdirAll(filepath.Join(data, "files"), 0700); err != nil {
		log.Fatal(err)
	}
	for _, f := range files {
		sum := sha256.Sum256([]byte(f.body))
		hash := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(data, "files", hash), []byte(f.body), 0600); err != nil {
			log.Fatal(err)
		}
		attachments = append(attachments, contract.Attachment{Name: f.name, Media: f.media, SHA256: hash})
	}
	post(owner, contract.PostRequest{Text: "Review these examples.", Attachments: attachments})
	// Seed a pending hostexec.run here when its contract arguments exist.
}
