package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
)

func TestVoiceHandoffIdleBusyAndFinal(t *testing.T) {
	s, room, _, base := setup(t)
	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	root := tree.Root()
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO accounts(id,proxy_id,provider,label) VALUES('voice_account','voice_proxy','codex','voice')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proxy, err := model.NewProxy(t.TempDir(), t.TempDir(), "management")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Listener.Close()
	s.searchProxy = proxy
	connections := make(chan *websocket.Conn, 1)
	proxyServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connections <- conn
		<-ctx.Done()
	})}
	go proxyServer.Serve(proxy.Listener)
	defer proxyServer.Close()
	run, problem := s.Voice(ctx, room)
	if problem != nil {
		t.Fatal(problem)
	}
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = run(conn)
	}))
	defer pageServer.Close()
	page, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(pageServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer page.CloseNow()
	var provider *websocket.Conn
	select {
	case provider = <-connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var session map[string]any
	if err := wsjson.Read(ctx, provider, &session); err != nil {
		t.Fatal(err)
	}
	sendTool := func(id, prompt string) {
		t.Helper()
		if err := wsjson.Write(ctx, provider, map[string]any{"type": "input_audio_buffer.committed", "item_id": "owner-" + id}); err != nil {
			t.Fatal(err)
		}
		args, _ := json.Marshal(map[string]string{"prompt": prompt})
		if err := wsjson.Write(ctx, provider, map[string]any{"type": "response.function_call_arguments.done", "name": "background_agent", "call_id": id, "arguments": string(args)}); err != nil {
			t.Fatal(err)
		}
	}
	finalItems := 0
	output := func(id string) string {
		t.Helper()
		for {
			var event struct {
				Type string `json:"type"`
				Item struct {
					Type    string `json:"type"`
					CallID  string `json:"call_id"`
					Output  string `json:"output"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			}
			if err := wsjson.Read(ctx, provider, &event); err != nil {
				t.Fatal(err)
			}
			for _, content := range event.Item.Content {
				if strings.Contains(content.Text, "voice final answer") {
					finalItems++
				}
			}
			if event.Item.Type == "function_call_output" && event.Item.CallID == id {
				return event.Item.Output
			}
		}
	}
	// Send adjacent calls without waiting for dispatch or a turn to start. The
	// first must own the handoff even when the scheduler has not started root.
	sendTool("idle", "start a voice task")
	sendTool("busy", "steer this task")
	if got := output("busy"); got != "This was sent to steer the previous background agent task." {
		t.Fatalf("busy output: %q", got)
	}
	events, err := s.store.Events(ctx, room, 0, 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	var posted []string
	for _, event := range events {
		if post, ok := event.Body.(contract.PostCreated); ok && post.Spoken {
			posted = append(posted, post.Text)
		}
	}
	if len(posted) != 2 || posted[0] != "start a voice task" || posted[1] != "steer this task" {
		t.Fatalf("owner post order: %q", posted)
	}
	start := nextTurn(t, s, ctx, root)
	if !strings.Contains(start.Inputs[0].Text, "start a voice task") {
		t.Fatalf("input: %+v", start.Inputs)
	}
	if _, problem := s.TurnOutput(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "voice final answer"}); problem != nil {
		t.Fatal(problem)
	}
	if got := output("idle"); got != "Background agent finished. Use the preceding [BACKEND] messages as the result." {
		t.Fatalf("idle output: %q", got)
	}
	if finalItems != 1 {
		t.Fatalf("final BACKEND items: %d", finalItems)
	}
	post, err := s.store.Event(ctx, int64(start.Inputs[0].Delivery))
	if err != nil {
		t.Fatal(err)
	}
	took, err := s.store.TurnTookPost(ctx, root, start.Turn, post.ID)
	if err != nil || !took {
		t.Fatalf("consumed handoff lookup: %v %v", took, err)
	}
	took, err = s.store.TurnTookPost(ctx, root, "unrelated_turn", post.ID)
	if err != nil || took {
		t.Fatalf("unrelated turn lookup: %v %v", took, err)
	}
	cancel()
}
