package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowPostDataBoundary(t *testing.T) {
	for _, size := range []int{1023, 1024, 1025} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			s.SetLayout(box.Layout{Data: t.TempDir()})
			data := `"` + strings.Repeat("x", size-2) + `"`
			var req contract.PostRequest
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"room":%q,"text":"summary","mentions":["root"],"clientId":"stable","data":%s}`, room, data)), &req); err != nil {
				t.Fatal(err)
			}
			caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
			sent, problem := s.Post(ctx, caller, req)
			if problem != nil {
				t.Fatal(problem)
			}
			again, problem := s.Post(ctx, caller, req)
			if problem != nil || again != sent {
				t.Fatalf("retry: %+v %v", again, problem)
			}
			event, err := s.store.Event(ctx, sent.Seq)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(event.Body)
			var body map[string]json.RawMessage
			_ = json.Unmarshal(wire, &body)
			if string(body["data"]) != data {
				t.Fatalf("data lost: %s", wire)
			}
			post := event.Body.(contract.PostCreated)
			if post.Text != "summary" {
				t.Fatalf("JSON wall in text: %q", post.Text)
			}
			wantFiles := 0
			if size > 1024 {
				wantFiles = 1
			}
			if len(post.Attachments) != wantFiles {
				t.Fatalf("files=%d want %d", len(post.Attachments), wantFiles)
			}
			var root contract.SessionID
			if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
				t.Fatal(err)
			}
			ch, p := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root})
			if p != nil {
				t.Fatal(p)
			}
			input := (<-ch).Body.(contract.TurnStart).Inputs[0]
			if size > 1024 {
				if strings.Contains(input.Text, data) || !strings.Contains(input.Text, fmt.Sprintf("data: /box/attachments/%d/data.json", sent.Seq)) || len(input.Files) != 1 {
					t.Fatalf("large data delivery: %+v", input)
				}

				proveDataDownload(t, s, ctx, room, root, input, []byte(data))
			} else if !strings.Contains(input.Text, data) || len(input.Files) != 0 {
				t.Fatalf("small data delivery: %+v", input)
			}
		})
	}
}

func TestWorkflowPostDataValues(t *testing.T) {
	for _, data := range []string{"null", "[]", "{}", "true", "42", `"雪🙂"`, `{"hostile":"</script><img src=x onerror=alert(1)>"}`} {
		t.Run(data, func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			s.SetLayout(box.Layout{Data: t.TempDir()})
			var req contract.PostRequest
			_ = json.Unmarshal([]byte(fmt.Sprintf(`{"room":%q,"text":"text","data":%s}`, room, data)), &req)
			sent, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, req)
			if p != nil {
				t.Fatal(p)
			}
			event, err := s.store.Event(ctx, sent.Seq)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(event.Body)
			var body map[string]json.RawMessage
			_ = json.Unmarshal(wire, &body)
			var want, got any
			_ = json.Unmarshal([]byte(data), &want)
			_ = json.Unmarshal(body["data"], &got)
			if len(body["data"]) == 0 || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("value lost: %s", wire)
			}
		})
	}
}

// The actual recipient Client downloads from the engine's authenticated file route over its Unix socket,
// to the exact path named in the input, under an isolated recipient root. The root mapping is test-only: CI hosts
// need not have a writable /box. No shared-directory shortcuts or metadata-only proof.
func proveDataDownload(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, recipient contract.SessionID, input contract.TurnInput, want []byte) {
	t.Helper()
	if len(input.Files) != 1 {
		t.Fatalf("want one recipient file: %+v", input.Files)
	}
	// Model the recipient box's root mount without requiring root permissions on a CI host.
	logical := input.Files[0].Path
	input.Files = append([]contract.TurnFile(nil), input.Files...)
	input.Files[0].Path = filepath.Join(t.TempDir(), strings.TrimPrefix(logical, "/"))
	input.Text = strings.ReplaceAll(input.Text, logical, input.Files[0].Path)
	file := input.Files[0]
	if _, err := os.Stat(file.Path); !os.IsNotExist(err) {
		t.Fatalf("recipient path already exists: %s %v", file.Path, err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Path); _ = os.Remove(filepath.Dir(file.Path)) })
	socket := filepath.Join(t.TempDir(), "recipient.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Files(contract.Principal{Kind: contract.PrincipalSession, Session: recipient}, room)
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	client := agent.NewClient(socket)
	for range 2 { // The same input replays without a second/partial file.
		got, err := client.Attach(ctx, input)
		if err != nil || got.Text != input.Text {
			t.Fatalf("Attach: %+v %v", got, err)
		}
		data, err := os.ReadFile(file.Path)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("recipient file %s: %q %v", file.Path, data, err)
		}
	}
	info, err := os.Stat(file.Path)
	if err != nil || info.Mode().Perm()&0022 != 0 {
		t.Fatalf("unsafe recipient file: %v %v", info, err)
	}
	t.Logf("recipient has %s (logical %s), %d exact bytes; authenticated download and replay passed", file.Path, logical, len(want))
}

func TestWorkflowFinalDataDelivery(t *testing.T) {
	for _, size := range []int{1023, 1024, 1025} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			w := setupWorkflow(t)
			w.SetLayout(box.Layout{Data: t.TempDir()})
			caller := contract.Principal{Kind: contract.PrincipalSession, Session: w.root}
			started, crew := w.start(caller, workflowScript)
			turn := w.turn(crew)
			data := `{"value":"` + strings.Repeat("雪", (size-12)/3) + strings.Repeat("x", (size-12)%3) + `"}`
			if len(data) != size {
				t.Fatalf("fixture bytes %d", len(data))
			}
			var output contract.TurnOutputRequest
			wire := fmt.Sprintf(`{"turn":%q,"outcome":"completed","final":%q,"data":%s}`, turn.Turn, data, data)
			if err := json.Unmarshal([]byte(wire), &output); err != nil {
				t.Fatal(err)
			}
			if _, p := w.TurnOutput(w.ctx, crew, output); p != nil {
				t.Fatal(p)
			}
			if _, p := w.TurnOutput(w.ctx, crew, output); p != nil {
				t.Fatalf("final retry: %v", p)
			}
			ch, p := testChannel(t, w.Service, w.ctx, caller)
			if p != nil {
				t.Fatal(p)
			}
			input := (<-ch).Body.(contract.TurnStart).Inputs[0]
			if size > 1024 {
				if strings.Contains(input.Text, data) || !strings.Contains(input.Text, "data: /box/attachments/") || !strings.Contains(input.Text, "Attached:") {
					t.Fatalf("final JSON wall/no file: %+v", input)
				}
				proveDataDownload(t, w.Service, w.ctx, w.room, w.root, input, []byte(data))
			} else if !strings.Contains(input.Text, "data:\n{") || len(input.Files) != 0 {
				t.Fatalf("small result missing inline: %+v", input)
			}
			status, p := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
			if p != nil || status.Run.Result != data {
				t.Fatalf("journal lost result: %+v %v", status.Run, p)
			}
		})
	}
}

func TestWorkflowPostDataContractsAndForward(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[1,{"conflicts":["雪🙂"]}]`), json.RawMessage(`{"groups":[{"conflicts":["` + strings.Repeat("雪🙂", 170) + `"]}]}`)} {
		req := contract.PostRequest{Room: room, Text: "text\n```json\n{not typed}\n```", Data: raw}
		sent, p := s.Post(ctx, caller, req)
		if p != nil {
			t.Fatal(p)
		}
		event, err := s.store.Event(ctx, sent.Seq)
		if err != nil {
			t.Fatal(err)
		}
		post := event.Body.(contract.PostCreated)
		if post.Text != req.Text || (len(post.Data) == 0) != (len(raw) == 0) {
			t.Fatalf("text/absence changed: %+v", post)
		}
		item := contract.TranscriptItem{}
		applyTranscriptPost(&item, event)
		text, _, files, data, err := transcriptInputBody(event)
		if err != nil || text != req.Text || string(data) != string(post.Data) || string(item.Data) != string(post.Data) || len(files) != len(post.Attachments) {
			t.Fatalf("transcript data lost: %+v %s %v", item, data, err)
		}
		listed, p := s.EventsList(ctx, caller, contract.EventsListRequest{Room: room, Event: sent.Event, Limit: 1})
		if p != nil || len(listed.Events) != 1 || string(listed.Events[0].Body.(contract.PostCreated).Data) != string(post.Data) {
			t.Fatalf("feed data lost: %+v %v", listed, p)
		}
		forwarded, p := s.Post(ctx, caller, contract.PostRequest{Room: room, Text: "note", Forward: sent.Event})
		if p != nil {
			t.Fatal(p)
		}
		e, err := s.store.Event(ctx, forwarded.Seq)
		if err != nil {
			t.Fatal(err)
		}
		f := e.Body.(contract.PostCreated)
		if string(f.Data) != string(post.Data) || len(f.Attachments) != len(post.Attachments) {
			t.Fatalf("forward data/files lost or duplicated: %+v", f)
		}
	}
	if _, p := s.Post(ctx, caller, contract.PostRequest{Room: room, Text: "invalid", Data: json.RawMessage(`not json`)}); p == nil {
		t.Fatal("invalid raw data accepted")
	}
}

func TestWorkflowHugeTranscriptDataHasReadableReference(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	raw := json.RawMessage(`{"conflicts":["` + strings.Repeat("雪", 100000) + `"]}`)
	sent, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "summary", Data: raw})
	if p != nil {
		t.Fatal(p)
	}
	event, err := s.store.Event(ctx, sent.Seq)
	if err != nil {
		t.Fatal(err)
	}
	item := contract.TranscriptItem{}
	applyTranscriptPost(&item, event)
	if len(item.Data) != 0 || item.DataRef == nil || item.DataRef.Summary != "1 keys" {
		t.Fatalf("oversized data is not referenced: %d inline bytes; reference present=%v", len(item.Data), item.DataRef != nil)
	}
	turn := contract.TranscriptTurn{Items: []contract.TranscriptItem{item}}
	budgetTranscriptTurn(&turn, contract.TranscriptReadRequest{}, 180<<10)
	if len(turn.Items) != 1 {
		t.Fatal("oversized data made post disappear from transcript")
	}
	bytes, err := os.ReadFile(s.layout.File(room, item.DataRef.File.SHA256))
	if err != nil || string(bytes) != string(raw) {
		t.Fatalf("reference lost data: %d %v", len(bytes), err)
	}
}

func TestWorkflowDataNullFinalJournal(t *testing.T) {
	w := setupWorkflow(t)
	w.SetLayout(box.Layout{Data: t.TempDir()})
	started, crew := w.start(w.owner, workflowScript)
	turn := w.turn(crew)
	if _, p := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: turn.Turn, Outcome: contract.TurnCompleted, Data: json.RawMessage(`null`)}); p != nil {
		t.Fatal(p)
	}
	status, p := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
	if p != nil || status.Run.Result != "null" {
		t.Fatalf("null lost: %+v %v", status.Run, p)
	}
}

func TestWorkflowDataAttachmentNameCollision(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	other := uploadFile(t, s, room, "data.json", "application/json", `{"unrelated":true}`)
	raw := json.RawMessage(`{"conflicts":["` + strings.Repeat("x", 1100) + `"]}`)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	sent, p := s.Post(ctx, caller, contract.PostRequest{Room: room, Text: "summary", Data: raw, Attachments: []contract.Attachment{other}})
	if p != nil {
		t.Fatal(p)
	}
	e, err := s.store.Event(ctx, sent.Seq)
	if err != nil {
		t.Fatal(err)
	}
	text := s.deliveryData(e, "summary")
	if !strings.Contains(text, fmt.Sprintf("data: /box/attachments/%d/data-2.json", sent.Seq)) {
		t.Fatalf("wrong actual path: %s", text)
	}
}

func TestWorkflowDataAttachmentContentWithWrongMedia(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	raw := json.RawMessage(`{"value":"` + strings.Repeat("x", 1100) + `"}`)
	other := uploadFile(t, s, room, "data.json", "text/plain", string(raw))
	sent, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "summary", Data: raw, Attachments: []contract.Attachment{other}})
	if p != nil {
		t.Fatal(p)
	}
	e, err := s.store.Event(ctx, sent.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.deliveryData(e, "summary"); !strings.Contains(got, fmt.Sprintf("data: /box/attachments/%d/data-2.json", sent.Seq)) {
		t.Fatalf("data with wrong media reused: %s", got)
	}
}
