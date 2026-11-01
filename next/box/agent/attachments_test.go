package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

func attachmentFile(t *testing.T, name, body string) contract.TurnFile {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return contract.TurnFile{Path: filepath.Join(t.TempDir(), "post", name), SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body))}
}

// fileClient serves handler where the box socket would be.
func fileClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	return NewClient(socket)
}

func TestAttachStoresAFileOnceByHash(t *testing.T) {
	file := attachmentFile(t, "notes.txt", "the notes")
	var requests atomic.Int32
	client := fileClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/files/"+file.SHA256 {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, "the notes")
	})
	input := contract.TurnInput{Delivery: 1, Text: "[Dyna] Frame.\nbody", Files: []contract.TurnFile{file}}
	for range 2 {
		got, err := client.Attach(context.Background(), input)
		if err != nil || got.Text != input.Text {
			t.Fatalf("%q, %v", got.Text, err)
		}
	}
	if data, err := os.ReadFile(file.Path); err != nil || string(data) != "the notes" {
		t.Fatalf("%q, %v", data, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("a replay fetched again: %d requests", requests.Load())
	}
	entries, err := os.ReadDir(filepath.Dir(file.Path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("left beside the file: %v, %v", entries, err)
	}
}

// A file the box cannot get must not cost the sender's words, and the frame says what is missing.
func TestAttachNotesAFileItCannotGet(t *testing.T) {
	gone, wrong := attachmentFile(t, "gone.txt", "gone"), attachmentFile(t, "wrong.txt", "right")
	client := fileClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/files/"+gone.SHA256 {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "wrong")
	})
	socketWait = time.Millisecond
	t.Cleanup(func() { socketWait = 5 * time.Minute })
	got, err := client.Attach(context.Background(), contract.TurnInput{Text: "[Dyna] Frame.\nbody\nmore", Files: []contract.TurnFile{gone, wrong}})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("[Dyna] Frame. %s could not be fetched, so it is not in the box. %s could not be fetched, so it is not in the box.\nbody\nmore", gone.Path, wrong.Path)
	if got.Text != want {
		t.Fatalf("got %q", got.Text)
	}
	for _, path := range []string{gone.Path, wrong.Path} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

// A relayed host drops connections and stalls. Neither loses the file.
func TestAttachRetriesABrokenOrStalledDownload(t *testing.T) {
	file := attachmentFile(t, "big.bin", strings.Repeat("x", 1<<20))
	fetchStall = 100 * time.Millisecond
	t.Cleanup(func() { fetchStall = time.Minute })
	var requests atomic.Int32
	client := fileClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(file.Bytes))
		w.(http.Flusher).Flush()
		switch requests.Add(1) {
		case 1: // the connection breaks partway
			fmt.Fprint(w, "xxxx")
			panic(http.ErrAbortHandler)
		case 2: // the connection stays open and sends nothing
			<-r.Context().Done()
		default:
			fmt.Fprint(w, strings.Repeat("x", 1<<20))
		}
	})
	got, err := client.Attach(context.Background(), contract.TurnInput{Text: "[Dyna] Frame.\nbody", Files: []contract.TurnFile{file}})
	if err != nil || got.Text != "[Dyna] Frame.\nbody" {
		t.Fatalf("%q, %v", got.Text, err)
	}
	if info, err := os.Stat(file.Path); err != nil || info.Size() != file.Bytes {
		t.Fatalf("%+v, %v", info, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("%d requests", requests.Load())
	}
}

// heldAttach blocks each download until the test lets it finish, and records nothing it did not store.
type heldAttach struct {
	entered chan contract.DeliverySeq
	release chan struct{}
}

func newHeldAttach() *heldAttach {
	return &heldAttach{entered: make(chan contract.DeliverySeq, 8), release: make(chan struct{}, 8)}
}

func (a *heldAttach) attach(ctx context.Context, input contract.TurnInput) (contract.TurnInput, error) {
	a.entered <- input.Delivery
	select {
	case <-a.release:
		input.Text += " with files"
		return input, nil
	case <-ctx.Done():
		return input, ctx.Err()
	}
}

// settlingHarness keeps a turn running until the test ends it.
type settlingHarness struct {
	*transferHarness
	inputs []contract.TurnInput
	end    chan struct{}
}

func (h *settlingHarness) Start(_ context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.inputs = inputs
	h.record("prompt")
	started()
	<-h.end
	return contract.TurnEnded{Turn: id, Outcome: "completed"}, nil
}

func (h *settlingHarness) Abort(context.Context) error {
	h.record("abort")
	close(h.end)
	return nil
}

func filedTurn(files bool) contract.BoxMessage {
	input := contract.TurnInput{Delivery: 1, Text: "start"}
	if files {
		input.Files = []contract.TurnFile{{Path: "/box/attachments/1/a"}}
	}
	return contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_1", Inputs: []contract.TurnInput{input}}}
}

func filedSteer(delivery contract.DeliverySeq, files bool) contract.BoxMessage {
	steer := contract.TurnSteer{Queue: contract.QueueSteer, Input: contract.TurnInput{Delivery: delivery, Text: fmt.Sprint("steer ", delivery)}}
	if files {
		steer.Input.Files = []contract.TurnFile{{Path: "/box/attachments/x"}}
	}
	return contract.BoxMessage{Body: steer}
}

// The prompt waits for its files, and Pi sees the input the download returned.
func TestRunnerStartsPiAfterTheFilesArrive(t *testing.T) {
	h := &settlingHarness{transferHarness: newTransferHarness(), end: make(chan struct{})}
	close(h.end)
	held := newHeldAttach()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink, Attach: held.attach}
	if err := runner.Handle(context.Background(), filedTurn(true)); err != nil {
		t.Fatal(err)
	}
	<-held.entered
	h.mu.Lock()
	early := len(h.calls)
	h.mu.Unlock()
	if early != 0 {
		t.Fatalf("Pi saw %d calls before the files arrived", early)
	}
	held.release <- struct{}{}
	<-sink.done
	if h.inputs[0].Text != "start with files" {
		t.Fatalf("inputs: %+v", h.inputs)
	}
}

func TestRunnerStopCancelsADownloadBeforeTheTurn(t *testing.T) {
	h := newTransferHarness()
	held := newHeldAttach()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink, Attach: held.attach}
	ctx := context.Background()
	if err := runner.Handle(ctx, filedTurn(true)); err != nil {
		t.Fatal(err)
	}
	<-held.entered
	h.aborted = make(chan struct{}, 1)
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStop{Turn: "turn_1"}}); err != nil {
		t.Fatal(err)
	}
	if got := <-sink.done; got.Outcome != "stopped" || got.Cause != contract.CauseNotStarted {
		t.Fatalf("turn: %+v", got)
	}
	if want := []string{"abort"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("Pi saw a stopped turn: %v", h.calls)
	}
}

// A steer's download must not hold the channel, and the steers behind it keep their order.
func TestRunnerKeepsSteersInOrderBehindADownload(t *testing.T) {
	h := &settlingHarness{transferHarness: newTransferHarness(), end: make(chan struct{})}
	held := newHeldAttach()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink, Attach: held.attach}
	ctx := context.Background()
	if err := runner.Handle(ctx, filedTurn(false)); err != nil {
		t.Fatal(err)
	}
	// Handle returning at all, with the download still blocked, is what proves the channel keeps reading.
	for _, message := range []contract.BoxMessage{filedSteer(2, true), filedSteer(3, false), filedSteer(4, true)} {
		if err := runner.Handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	<-held.entered
	held.release <- struct{}{}
	<-held.entered
	held.release <- struct{}{}
	deadline := time.After(5 * time.Second)
	for {
		h.mu.Lock()
		calls := len(h.calls)
		h.mu.Unlock()
		if calls == 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("steers did not reach Pi")
		case <-time.After(time.Millisecond):
		}
	}
	close(h.end)
	got := <-sink.done
	if len(got.Unread) != 0 {
		t.Fatalf("unread: %v", got.Unread)
	}
	if want := []string{"prompt", "steer 2 with files", "steer 3", "steer 4 with files"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls: %v", h.calls)
	}
}

// A turn can end while a steer's files are on the way. Pi would never read that steer, so the engine gets it back.
func TestRunnerHandsBackASteerTheTurnEndedWithout(t *testing.T) {
	h := &settlingHarness{transferHarness: newTransferHarness(), end: make(chan struct{})}
	held := newHeldAttach()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink, Attach: held.attach}
	ctx := context.Background()
	if err := runner.Handle(ctx, filedTurn(false)); err != nil {
		t.Fatal(err)
	}
	for _, message := range []contract.BoxMessage{filedSteer(2, true), filedSteer(3, false)} {
		if err := runner.Handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	<-held.entered
	close(h.end)
	got := <-sink.done
	if want := []contract.DeliverySeq{2, 3}; !reflect.DeepEqual(got.Unread, want) {
		t.Fatalf("unread: %v", got.Unread)
	}
	if want := []string{"prompt"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls: %v", h.calls)
	}
}

var _ harness.Harness = (*settlingHarness)(nil)
