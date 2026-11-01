//go:build linux

package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
)

type boxProfiles struct{ fakeProfiles }

func (boxProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "claude-opus-4-5", Thinking: contract.ThinkingLow}, []string{"claude-opus-4-5"}, nil
}

// boxService adds what the runtime adds to the service. Boxes call none of it.
type boxService struct {
	*Service
	*AccountActions
}

func (boxService) AccountsQuota(context.Context, contract.Principal, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error) {
	return contract.AccountsQuotaResponse{}, nil
}

func (boxService) HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	return contract.HostAddResponse{}, nil
}

// localRunner runs the host's commands here, where a real host would run them over SSH.
type localRunner struct{}

func (localRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") {
		return nil, nil
	}
	c := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	c.Stdin = bytes.NewReader(input)
	return c.Output()
}

// slowRelay stands in for a relayed host's forwarded socket: every byte toward the box is limited to rate per second.
func slowRelay(t *testing.T, listen, engine string, rate int) {
	t.Helper()
	l, err := net.Listen("unix", listen)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				target, err := net.Dial("unix", engine)
				if err != nil {
					return
				}
				defer target.Close()
				go func() { _, _ = io.Copy(target, conn) }()
				buf := make([]byte, rate/20)
				for {
					n, err := target.Read(buf)
					if n > 0 {
						if _, err := conn.Write(buf[:n]); err != nil {
							return
						}
						time.Sleep(time.Duration(n) * time.Second / time.Duration(rate))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
}

// scriptedModel answers a real Pi in a real box. Each request runs reply, which returns "" for text or a path to read.
type scriptedModel struct {
	mu     sync.Mutex
	bodies []string
	reply  func(body string) string
	hold   chan struct{} // when set, a request waits here before it is answered
}

func (m *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.bodies = append(m.bodies, string(data))
	reply, hold := m.reply, m.hold
	m.mu.Unlock()
	if hold != nil {
		<-hold
	}
	path := ""
	if reply != nil {
		path = reply(string(data))
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
	if path != "" {
		arguments, _ := json.Marshal(map[string]string{"path": path})
		input, _ := json.Marshal(string(arguments))
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`)
		send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, input))
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`)
		send("message_stop", `{"type":"message_stop"}`)
		return
	}
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Seen."}}`)
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

func (m *scriptedModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

func (m *scriptedModel) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bodies[len(m.bodies)-1]
}

func (m *scriptedModel) set(reply func(string) string, hold chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reply, m.hold = reply, hold
}

// One session in a real box on real Podman with the real box agent and the Pi the image ships, over a slow relayed socket:
// the agent reads a text file with its own read tool and sees an image, a post written while the box is stopped reaches
// it on waking with its big file intact, and a steer brings another image into a running turn.
//
// DYNA_BOX_TEST_IMAGE names a local dyna-base image. DYNA_BOX_TEST_BINARY names a static dyna build.
func TestAttachmentsInARealBox(t *testing.T) {
	image, binary := os.Getenv("DYNA_BOX_TEST_IMAGE"), os.Getenv("DYNA_BOX_TEST_BINARY")
	if image == "" || binary == "" {
		t.Skip("DYNA_BOX_TEST_IMAGE and DYNA_BOX_TEST_BINARY name a dyna-base image and a dyna build; this test runs a real box")
	}
	s, room, session, base := setup(t)
	s.profiles = boxProfiles{}
	ctx, cancel := context.WithTimeout(base, 12*time.Minute)
	defer cancel()
	data, err := os.MkdirTemp("", "dyna-att-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: filepath.Join(data, "host")}
	s.SetLayout(layout)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	id := record.Box

	model := &scriptedModel{}
	server := api.New(boxService{Service: s}, func(api.Caller) http.Handler { return model })
	server.SetFilesRoute(func(c api.Caller) http.Handler { return s.Files(c.Principal, c.Room) })
	runtimeDir := filepath.Join(data, "rt")
	sockets := api.NewSockets(server, runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	if err := sockets.AddBox(id, session, room); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.HostSocketFolder(id), 0755); err != nil {
		t.Fatal(err)
	}
	slowRelay(t, layout.HostSocket(id), filepath.Join(runtimeDir, "boxes", string(id)+".sock"), 4<<20)

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
	slice := fmt.Sprintf("dyna-att-test-%d.slice", time.Now().UnixNano())
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("podman", "logs", "--tail", "40", string(id)).CombinedOutput()
			t.Logf("box log:\n%s", out)
		}
		_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
		_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
		_ = host.RemoveSlice(context.Background(), slice)
	})
	if err := host.EnsureSlice(ctx, box.Slice{Name: slice, CPUs: 2, MemHigh: 2 << 30, MemMax: 3 << 30, Pids: 1024}); err != nil {
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
	inBox := func(script string) string {
		t.Helper()
		out, err := exec.Command("podman", "exec", string(id), "bash", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("in the box, %s: %v: %s", script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	endedTurns := func() int {
		t.Helper()
		listed, problem := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"turn.ended"}, Limit: 1000})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, event := range listed.Events {
			if ended := event.Body.(contract.TurnEnded); ended.Outcome != contract.TurnCompleted && ended.Session == session {
				t.Fatalf("turn %+v", ended)
			}
		}
		return len(listed.Events)
	}
	waitTurns := func(want int) {
		t.Helper()
		for endedTurns() < want {
			if ctx.Err() != nil {
				t.Fatalf("waited for %d turns, have %d, after %d model requests", want, endedTurns(), model.count())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitRequests := func(want int) {
		t.Helper()
		for model.count() < want {
			if ctx.Err() != nil {
				t.Fatalf("waited for %d model requests, have %d", want, model.count())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	post := func(text string, files ...contract.Attachment) contract.PostResponse {
		t.Helper()
		sent, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: session}, Text: text, Attachments: files})
		if problem != nil {
			t.Fatal(problem)
		}
		return sent
	}
	images := func(body string) []string {
		var shown []string
		for _, one := range readAnthropicRequest(t, body).images {
			shown = append(shown, one.data)
		}
		return shown
	}

	// The first post is waiting when the box first starts.
	shot := noisePNG(t, 500, 300)
	first := post("read the notes and look at this", uploadFile(t, s, room, "image.png", "image/png", string(shot)), uploadFile(t, s, room, "notes.txt", "text/plain", "the words in the file"))
	notes := fmt.Sprintf("/box/attachments/%d/notes.txt", first.Seq)
	readOnce := func(body string) string {
		if strings.Contains(body, "the words in the file") {
			return ""
		}
		return notes
	}
	model.set(readOnce, nil)
	if err := host.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitTurns(1)
	last := model.last()
	if !strings.Contains(last, "the words in the file") {
		t.Fatal("Pi's read tool did not return the file to the model")
	}
	encoded := base64.StdEncoding.EncodeToString(shot)
	if got := images(last); !slices.Contains(got, encoded) {
		t.Fatalf("%d images in the request", len(got))
	}
	if got := inBox("cat " + notes); got != "the words in the file" {
		t.Fatalf("notes %q", got)
	}
	if got := inBox("id -u"); got != "1000" {
		t.Fatalf("uid %q", got)
	}

	// A post written while the box is stopped waits for it, with a file too big to arrive quickly.
	if err := host.StopContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	second := noisePNG(t, 400, 400)
	bigFile := bytes.Repeat([]byte("0123456789abcdef"), 1<<18+1<<17) // 6 MiB through a 4 MiB/s relay
	sum := sha256.Sum256(bigFile)
	woken := post("this came while you slept", uploadFile(t, s, room, "second.png", "image/png", string(second)), uploadFile(t, s, room, "big.bin", "application/octet-stream", string(bigFile)))
	turns := endedTurns()
	model.set(nil, nil)
	startedAt := time.Now()
	if err := host.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitTurns(turns + 1)
	t.Logf("wake to end of turn: %s", time.Since(startedAt))
	got := images(model.last())
	if !slices.Contains(got, base64.StdEncoding.EncodeToString(second)) || !slices.Contains(got, encoded) {
		t.Fatalf("%d images after the wake: both must be in the request", len(got))
	}
	if !strings.Contains(model.last(), fmt.Sprintf("/box/attachments/%d/big.bin (application/octet-stream, 6.0 MB)", woken.Seq)) {
		t.Fatal("the frame does not name the big file")
	}
	if got := inBox(fmt.Sprintf("sha256sum /box/attachments/%d/big.bin", woken.Seq)); !strings.HasPrefix(got, hex.EncodeToString(sum[:])) {
		t.Fatalf("big file %q", got)
	}

	// A steer brings a file into a running turn. The model holds its first request until Pi has the steer.
	hold := make(chan struct{})
	before := model.count()
	turns = endedTurns()
	model.set(func(body string) string {
		if strings.Contains(body, "and this one too") {
			return ""
		}
		return "/box/attachments/does-not-matter"
	}, hold)
	post("start something")
	waitRequests(before + 1)
	steered := noisePNG(t, 300, 300)
	steer := post("and this one too", uploadFile(t, s, room, "steered.png", "image/png", string(steered)))
	path := fmt.Sprintf("/box/attachments/%d/steered.png", steer.Seq)
	for exec.Command("podman", "exec", string(id), "test", "-e", path).Run() != nil {
		if ctx.Err() != nil {
			t.Fatal("the steered file never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The file is in the box a moment before Pi has the steer.
	time.Sleep(time.Second)
	close(hold)
	waitTurns(turns + 1)
	if got := images(model.last()); !slices.Contains(got, base64.StdEncoding.EncodeToString(steered)) {
		t.Fatalf("%d images after the steer", len(got))
	}

	// All of that left only text in the session file, which the engine copies.
	size := inBox("cat /box/pi/sessions/*.jsonl | wc -c")
	var transcript int
	if _, err := fmt.Sscan(size, &transcript); err != nil {
		t.Fatal(err)
	}
	if transcript > len(shot)/2 {
		t.Fatalf("the transcript is %d bytes after a %d byte image", transcript, len(shot))
	}
	t.Logf("session file: %d bytes after three images of %d bytes", transcript, len(shot)+len(second)+len(steered))
}
