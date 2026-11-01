//go:build linux

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

type piProfiles struct{ fakeProfiles }

func (piProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "m", Thinking: contract.ThinkingLow}, []string{"m"}, nil
}

// serviceSink reports to the engine as the box client does.
type serviceSink struct {
	s      *Service
	caller contract.Principal
	ended  chan contract.TurnEnded
}

func (k serviceSink) DeliveryDone(ctx context.Context, id contract.DeliverySeq) error {
	if _, problem := k.s.DeliveryDone(ctx, k.caller, contract.DeliveryDoneRequest{Delivery: id}); problem != nil {
		return errors.New(problem.Message)
	}
	return nil
}

func (k serviceSink) TurnOutput(ctx context.Context, result contract.TurnEnded) error {
	if _, problem := k.s.TurnOutput(ctx, k.caller, contract.TurnOutputRequest{Turn: result.Turn, Outcome: result.Outcome, Final: result.Final, Error: result.Error, Cause: result.Cause, Model: result.Usage.Model, Usage: result.Usage, Unread: result.Unread}); problem != nil {
		return errors.New(problem.Message)
	}
	k.ended <- result
	return nil
}

// recordingProvider answers every Anthropic request with text and keeps the bodies, in the order they came.
type recordingProvider struct {
	mu     sync.Mutex
	bodies []string
}

func (p *recordingProvider) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(data))
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Seen."}}`)
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

func (p *recordingProvider) last(t *testing.T) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.bodies) == 0 {
		t.Fatal("no model request")
	}
	return p.bodies[len(p.bodies)-1]
}

// piBox is the box side of a turn, outside a container: the real Pi with the real extension, a real box client on a
// real socket, and the engine's real channel.
type piBox struct {
	s       *Service
	ctx     context.Context
	room    contract.RoomID
	caller  contract.Principal
	client  *agent.Client
	dir     string
	args    []string
	ended   chan contract.TurnEnded
	channel <-chan contract.BoxMessage
}

func newPiBox(t *testing.T, serve http.HandlerFunc, modelDefinition map[string]any) *piBox {
	t.Helper()
	cli := os.Getenv("DYNA_PI_E2E_CLI")
	if cli == "" {
		t.Skip("DYNA_PI_E2E_CLI names a built Pi cli.js; this test runs that real Pi")
	}
	s, base, caller := transcriptCaller(t)
	s.profiles = piProfiles{}
	ctx, cancel := context.WithTimeout(base, 4*time.Minute)
	t.Cleanup(cancel)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	folder := box.AttachmentFolder
	box.AttachmentFolder = filepath.Join(t.TempDir(), "attachments")
	t.Cleanup(func() { box.AttachmentFolder = folder })

	dir := t.TempDir()
	socket := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	files := &http.Server{Handler: s.Files(caller, session.Room)}
	go files.Serve(listener)
	t.Cleanup(func() { _ = files.Close() })

	model := httptest.NewServer(serve)
	t.Cleanup(model.Close)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(dir, "agent"))
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("PI_NO_ENV_CREDENTIALS", "1")
	definition := map[string]any{"id": "m", "name": "m", "api": "anthropic-messages", "reasoning": true, "input": []string{"text", "image"}, "contextWindow": 1000000, "maxTokens": 8000, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}
	for key, value := range modelDefinition {
		definition[key] = value
	}
	models, err := json.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{"baseUrl": model.URL, "apiKey": "box", "models": []any{definition}}}})
	if err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(dir, "models.json")
	extension := filepath.Join(dir, "dyna.js")
	sessions := filepath.Join(dir, "sessions")
	if err := os.WriteFile(modelsPath, models, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte(agent.RenderExtension()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	return &piBox{
		s: s, ctx: ctx, room: session.Room, caller: caller, client: agent.NewClient(socket), dir: dir, ended: make(chan contract.TurnEnded, 8),
		args: []string{cli, "--provider", "anthropic", "--model", "m", "--thinking", "low", "--mode", "rpc", "--extension", extension, "--models-file", modelsPath, "--session-dir", sessions, "--exclude-tools", "bash", "--continue"},
	}
}

// start runs one Pi process behind a real runner, as the box agent does, until the test ends or the returned stop runs.
func (b *piBox) start(t *testing.T, wrap func(*pi.Harness) *observedHarness) (*pi.Harness, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(b.ctx)
	h, err := pi.Open(ctx, "node", b.args, "", b.dir)
	if err != nil {
		t.Fatal(err)
	}
	stop := sync.OnceFunc(func() { cancel(); _ = h.Close() })
	t.Cleanup(stop)
	if b.channel == nil {
		var problem *contract.Error
		if b.channel, problem = testChannel(t, b.s, b.ctx, b.caller); problem != nil {
			t.Fatal(problem)
		}
	}
	runner := &agent.Runner{Harness: h, Sink: serviceSink{b.s, b.caller, b.ended}, Attach: b.client.Attach}
	if wrap != nil {
		runner.Harness = wrap(h)
	}
	go func() {
		for {
			select {
			case message := <-b.channel:
				switch message.Body.(type) {
				case contract.TurnStart, contract.TurnSteer, contract.TurnStop:
					if err := runner.Handle(ctx, message); err != nil && ctx.Err() == nil {
						t.Errorf("handle: %v", err)
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return h, stop
}

func (b *piBox) waitTurn(t *testing.T) contract.TurnEnded {
	t.Helper()
	select {
	case ended := <-b.ended:
		if ended.Outcome != contract.TurnCompleted {
			t.Fatalf("turn %+v", ended)
		}
		return ended
	case <-b.ctx.Done():
		t.Fatal("the turn did not end")
		return contract.TurnEnded{}
	}
}

func (b *piBox) post(t *testing.T, text string, files ...contract.Attachment) contract.PostResponse {
	t.Helper()
	sent, problem := b.s.Post(b.ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: b.room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: b.caller.Session}, Text: text, Attachments: files})
	if problem != nil {
		t.Fatal(problem)
	}
	return sent
}

func noisePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{uint8(rand.IntN(256)), uint8(rand.IntN(256)), uint8(rand.IntN(256)), 255})
		}
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// anthropicRequest is what a request body holds in image blocks and text.
type anthropicRequest struct {
	images []struct{ media, data string }
	text   string
}

func readAnthropicRequest(t *testing.T, body string) anthropicRequest {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	var out anthropicRequest
	for _, message := range request.Messages {
		var blocks []struct {
			Type, Text string
			Source     struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			out.text += string(message.Content) + "\n"
			continue
		}
		for _, block := range blocks {
			if block.Type == "image" {
				out.images = append(out.images, struct{ media, data string }{block.Source.MediaType, block.Source.Data})
			}
			out.text += block.Text + "\n"
		}
	}
	return out
}

func sessionBytes(t *testing.T, h *pi.Harness) []byte {
	t.Helper()
	data, err := os.ReadFile(h.SessionFile())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A post written while the box was away arrives in the batch that wakes it. The model sees the real image, the other file
// is in the box, and the session file, which is the transcript, has none of the bytes. The next turn and a new Pi process
// still show the model that image.
func TestRealPiShowsAnAttachedImageToTheModel(t *testing.T) {
	provider := &recordingProvider{}
	b := newPiBox(t, provider.serve, nil)
	shot := noisePNG(t, 600, 400)
	image := uploadFile(t, b.s, b.room, "image.png", "image/png", string(shot))
	notes := uploadFile(t, b.s, b.room, "notes.txt", "text/plain", "the second file")
	sent := b.post(t, "what does this show?", image, notes)

	h, stop := b.start(t, nil)
	b.waitTurn(t)
	encoded := base64.StdEncoding.EncodeToString(shot)
	request := readAnthropicRequest(t, provider.last(t))
	if len(request.images) != 1 || request.images[0].media != "image/png" || request.images[0].data != encoded {
		t.Fatalf("%d images in the request", len(request.images))
	}
	folder := filepath.Join(box.AttachmentFolder, fmt.Sprint(sent.Seq))
	if !strings.Contains(request.text, folder+"/image.png (image/png, ") || !strings.Contains(request.text, folder+"/notes.txt (text/plain, 1 KB)") {
		t.Fatalf("frame in the request: %s", request.text)
	}
	if data, err := os.ReadFile(filepath.Join(folder, "notes.txt")); err != nil || string(data) != "the second file" {
		t.Fatalf("%q, %v", data, err)
	}
	transcript := sessionBytes(t, h)
	if bytes.Contains(transcript, []byte(encoded[:64])) || len(transcript) > len(shot)/10 {
		t.Fatalf("the transcript took the image: %d bytes for a %d byte image", len(transcript), len(shot))
	}

	b.post(t, "and now?")
	b.waitTurn(t)
	if got := readAnthropicRequest(t, provider.last(t)); len(got.images) != 1 || got.images[0].data != encoded {
		t.Fatalf("next turn: %d images", len(got.images))
	}

	stop()
	b.start(t, nil)
	b.post(t, "after a restart?")
	b.waitTurn(t)
	if got := readAnthropicRequest(t, provider.last(t)); len(got.images) != 1 || got.images[0].data != encoded {
		t.Fatalf("after a restart: %d images", len(got.images))
	}
}

// observedHarness tells the test when Pi has taken a steer.
type observedHarness struct {
	*pi.Harness
	steered chan struct{}
}

func (h *observedHarness) Steer(ctx context.Context, input contract.TurnInput) error {
	err := h.Harness.Steer(ctx, input)
	h.steered <- struct{}{}
	return err
}

// A steer reaches the model on Pi's next request, between tool calls, and the file is in the box by then.
func TestRealPiShowsASteeredImageToTheModel(t *testing.T) {
	provider := &steerProvider{release: make(chan struct{})}
	b := newPiBox(t, provider.serve, nil)
	observed := make(chan struct{}, 1)
	h, _ := b.start(t, func(h *pi.Harness) *observedHarness { return &observedHarness{h, observed} })
	for {
		provider.mu.Lock()
		running := len(provider.bodies) > 0
		provider.mu.Unlock()
		if running {
			break
		}
		select {
		case <-b.ctx.Done():
			t.Fatal("the first request never came")
		case <-time.After(10 * time.Millisecond):
		}
	}
	shot := noisePNG(t, 300, 200)
	sent := b.post(t, "look at this too", uploadFile(t, b.s, b.room, "steered.png", "image/png", string(shot)), uploadFile(t, b.s, b.room, "steered.log", "text/plain", "log line"))
	select {
	case <-observed:
	case <-b.ctx.Done():
		t.Fatal("the steer did not reach Pi")
	}
	close(provider.release)
	b.waitTurn(t)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.bodies) != 2 {
		t.Fatalf("%d model requests", len(provider.bodies))
	}
	if got := readAnthropicRequest(t, provider.bodies[0]); len(got.images) != 0 {
		t.Fatalf("the first request has %d images", len(got.images))
	}
	got := readAnthropicRequest(t, provider.bodies[1])
	if len(got.images) != 1 || got.images[0].data != base64.StdEncoding.EncodeToString(shot) {
		t.Fatalf("the steered request has %d images", len(got.images))
	}
	if data, err := os.ReadFile(filepath.Join(box.AttachmentFolder, fmt.Sprint(sent.Seq), "steered.log")); err != nil || string(data) != "log line" {
		t.Fatalf("%q, %v", data, err)
	}
	if transcript := sessionBytes(t, h); len(transcript) > len(shot)/2 {
		t.Fatalf("the transcript took the image: %d bytes", len(transcript))
	}
}

// Sixteen big images, one too wide, one corrupt, and one over the model's byte limit: the turn still runs, the request stays
// under the model route's limit, and the frame's promises are kept or corrected in the request.
func TestRealPiKeepsManyImagesWithinOneRequest(t *testing.T) {
	provider := &recordingProvider{}
	b := newPiBox(t, provider.serve, nil)
	var files []contract.Attachment
	files = append(files, uploadFile(t, b.s, b.room, "wide.png", "image/png", string(noisePNG(t, 2400, 200))))
	files = append(files, uploadFile(t, b.s, b.room, "corrupt.png", "image/png", pngHead+"this is not an image"))
	big := noisePNG(t, 1500, 1500) // about 6 MiB, over Pi's 4.5 MiB limit for one image
	files = append(files, uploadFile(t, b.s, b.room, "big.png", "image/png", string(big)))
	for i := range 13 {
		files = append(files, uploadFile(t, b.s, b.room, "shot.png", "image/png", string(noisePNG(t, 900, 900))))
		_ = i
	}
	sent := b.post(t, "all of these", files...)
	h, _ := b.start(t, nil)
	b.waitTurn(t)
	body := provider.last(t)
	if len(body) > 32<<20 {
		t.Fatalf("a %d byte request cannot pass the model route", len(body))
	}
	request := readAnthropicRequest(t, body)
	folder := filepath.Join(box.AttachmentFolder, fmt.Sprint(sent.Seq))
	total := 0
	for _, shown := range request.images {
		total += len(shown.data)
		if len(shown.data) > 9<<19 {
			t.Fatalf("a %d byte image is over the model's limit", len(shown.data))
		}
	}
	if len(request.images) < 2 || total > 20<<20 {
		t.Fatalf("%d images, %d bytes", len(request.images), total)
	}
	first, err := base64.StdEncoding.DecodeString(request.images[0].data)
	if err != nil {
		t.Fatal(err)
	}
	wide, _, err := image.DecodeConfig(bytes.NewReader(first))
	if err != nil || wide.Width != 2000 {
		t.Fatalf("the wide image: %+v, %v", wide, err)
	}
	for _, part := range []string{
		"[Image: original 2400x200, displayed at 2000x167.",
		"could not shrink " + folder + "/corrupt.png",
		"[" + folder + "/shot-13.png is left out of this request to save space. Read the file to see it.]",
	} {
		if !strings.Contains(request.text, part) {
			t.Errorf("missing %q in %s", part, request.text)
		}
	}
	for _, name := range []string{"wide.png", "corrupt.png", "big.png", "shot.png", "shot-13.png"} {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Error(err)
		}
	}
	if transcript := sessionBytes(t, h); len(transcript) > 64<<10 {
		t.Fatalf("the transcript took %d bytes", len(transcript))
	}
	// The next post's own image is the newest, so it comes first and pushes an old one out.
	shot := noisePNG(t, 300, 200)
	b.post(t, "and this", uploadFile(t, b.s, b.room, "new.png", "image/png", string(shot)))
	b.waitTurn(t)
	found := false
	for _, shown := range readAnthropicRequest(t, provider.last(t)).images {
		found = found || shown.data == base64.StdEncoding.EncodeToString(shot)
	}
	if !found {
		t.Fatal("the newest image is not in the request")
	}
}

// A session's post carries a file to another session like the owner's does.
func TestRealPiShowsASessionsImageToItsParent(t *testing.T) {
	provider := &recordingProvider{}
	b := newPiBox(t, provider.serve, nil)
	child, problem := b.s.SessionStart(b.ctx, b.caller, contract.SessionStartRequest{Room: b.room, Name: "child", Profile: "test", Parent: b.caller.Session, Goal: "goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	shot := noisePNG(t, 300, 200)
	file := uploadFile(t, b.s, b.room, "from-child.png", "image/png", string(shot))
	sender := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	if _, problem := b.s.Post(b.ctx, sender, contract.PostRequest{Room: b.room, Text: "my result", Mentions: []string{"parent"}, Attachments: []contract.Attachment{file}}); problem != nil {
		t.Fatal(problem)
	}
	h, _ := b.start(t, nil)
	b.waitTurn(t)
	got := readAnthropicRequest(t, provider.last(t))
	if len(got.images) != 1 || got.images[0].data != base64.StdEncoding.EncodeToString(shot) || !strings.Contains(got.text, `From your child "child", in the room feed. Attached: `) {
		t.Fatalf("%d images: %s", len(got.images), got.text)
	}
	if transcript := sessionBytes(t, h); len(transcript) > len(shot)/2 {
		t.Fatalf("the transcript took the image: %d bytes", len(transcript))
	}
}

// Pi's other API, the one Codex models use, carries the same image as a data URL.
func TestRealPiSendsAnImageOverTheResponsesAPI(t *testing.T) {
	bodies := make(chan string, 8)
	b := newPiBox(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies <- r.URL.Path + " " + string(data)
		http.Error(w, "the test has what it needs", http.StatusBadRequest)
	}, map[string]any{"api": "openai-responses"})
	shot := noisePNG(t, 300, 200)
	b.post(t, "over responses", uploadFile(t, b.s, b.room, "image.png", "image/png", string(shot)))
	b.start(t, nil)
	select {
	case body := <-bodies:
		if !strings.Contains(body, `"type":"input_image"`) || !strings.Contains(body, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(shot)) {
			t.Fatalf("no image in %d bytes: %.300s", len(body), body)
		}
	case <-b.ctx.Done():
		t.Fatal("no model request")
	}
}
