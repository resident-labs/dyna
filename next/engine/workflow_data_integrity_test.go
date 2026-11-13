package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/engine/transcriptview"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
)

func TestWorkflowCanonicalDataWireAndForward(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	for _, raw := range []string{`{"n":9007199254740993,"e":1e0,"zero":-0}`, `null`, `"null"`, `"\u0061"`, `{"n":1e0,"pad":"` + strings.Repeat("x", 301-len(`{"n":1e0,"pad":""}`)) + `"}`} {
		t.Run(fmt.Sprint(len(raw)), func(t *testing.T) {
			// This is the JSON body a normal browser JSON.stringify sends after reading the encoded field.
			wire, _ := json.Marshal(map[string]any{"room": room, "text": "summary", "data": raw})
			var req contract.PostRequest
			if err := json.Unmarshal(wire, &req); err != nil {
				t.Fatal(err)
			}
			sent, p := s.Post(ctx, caller, req)
			if p != nil {
				t.Fatal(p)
			}
			event, err := s.store.Event(ctx, sent.Seq)
			if err != nil {
				t.Fatal(err)
			}
			post := event.Body.(contract.PostCreated)
			if string(post.Data) != raw {
				t.Fatalf("canonical wire data changed: got %s want %s", post.Data, raw)
			}
			encoded, _ := json.Marshal(post)
			var browser struct {
				Data string `json:"data"`
			}
			if err := json.Unmarshal(encoded, &browser); err != nil || browser.Data != raw {
				t.Fatalf("API read lost canonical text: %s %v", encoded, err)
			}
			forwarded, p := s.Post(ctx, caller, contract.PostRequest{Room: room, Text: "note", Forward: sent.Event})
			if p != nil {
				t.Fatal(p)
			}
			again, err := s.store.Event(ctx, forwarded.Seq)
			if err != nil {
				t.Fatal(err)
			}
			if string(again.Body.(contract.PostCreated).Data) != raw {
				t.Fatal("server forward changed canonical bytes")
			}
			// Browser forwarding sends the same string, without parsing the number tokens.
			resend, _ := json.Marshal(map[string]any{"room": room, "text": "browser forward", "data": browser.Data})
			if err := json.Unmarshal(resend, &req); err != nil {
				t.Fatal(err)
			}
			second, p := s.Post(ctx, caller, req)
			if p != nil {
				t.Fatal(p)
			}
			again, err = s.store.Event(ctx, second.Seq)
			if err != nil {
				t.Fatal(err)
			}
			if string(again.Body.(contract.PostCreated).Data) != raw {
				t.Fatal("browser forward changed canonical bytes")
			}
		})
	}
}

func TestWorkflowTranscriptDataOwnership(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	raw := json.RawMessage(`{"n":9007199254740993,"v":"` + strings.Repeat("x", 1100) + `"}`)
	other := uploadFile(t, s, room, "data.json", "application/json", `{"unrelated":true}`)
	sent, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "summary", Data: contract.JSONData(raw), Attachments: []contract.Attachment{other}, Mentions: []string{"root"}})
	if p != nil {
		t.Fatal(p)
	}
	event, err := s.store.Event(ctx, sent.Seq)
	if err != nil {
		t.Fatal(err)
	}
	post := event.Body.(contract.PostCreated)
	if post.DataAttachment == nil || *post.DataAttachment != 1 {
		t.Fatal("fixture has no second associated file")
	}
	check := func(t *testing.T, value any) {
		t.Helper()
		wire, _ := json.Marshal(value)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(wire, &body)
		var encoded string
		_ = json.Unmarshal(body["data"], &encoded)
		if encoded != string(raw) {
			t.Fatalf("transcript changed canonical text: %s", encoded)
		}
		if string(body["dataAttachment"]) != "1" {
			t.Fatalf("transcript ownership lost: %s", wire)
		}
	}
	t.Run("post adapter", func(t *testing.T) {
		item := contract.TranscriptItem{}
		transcriptview.ApplyPost(&item, event)
		check(t, item)
	})
	t.Run("steer adapter", func(t *testing.T) {
		item := pig.TranscriptItem{}
		if err := transcriptview.ApplySteer(&item, event, false); err != nil {
			t.Fatal(err)
		}
		check(t, item.TranscriptItem)
	})
	t.Run("read endpoint", func(t *testing.T) {
		var root contract.SessionID
		if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
			t.Fatal(err)
		}
		ch, p := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root})
		if p != nil {
			t.Fatal(p)
		}
		turn := (<-ch).Body.(contract.TurnStart)
		response, p := s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.TranscriptReadRequest{Session: root, Turn: turn.Turn})
		if p != nil {
			t.Fatal(p)
		}
		for _, turn := range response.Turns {
			for _, input := range turn.Inputs {
				if input.ID == sent.Event {
					check(t, input)
					return
				}
			}
		}
		t.Fatal("post missing from transcript endpoint")
	})
}

func TestWorkflowPostDataUTF8Boundary(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	for _, bad := range [][]byte{{0xff}, {0xc0, 0xaf}, {0xe2, 0x82}} {
		raw := append([]byte(`{"v":"`), bad...)
		raw = append(raw, []byte(`"}`)...)
		if _, _, _, err := s.preparePostData(ctx, room, contract.JSONData(raw), nil); err == nil {
			t.Errorf("accepted invalid literal UTF-8: %x", bad)
		}
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`{"v":"雪🙂ÿ"}`), json.RawMessage(`{"v":"\u00ff\ud800"}`)} {
		got, _, _, err := s.preparePostData(ctx, room, contract.JSONData(raw), nil)
		if err != nil || !utf8.ValidString(string(got)) || !bytes.Equal([]byte(got), raw) {
			t.Fatalf("valid UTF-8 or escape changed: %s %v", got, err)
		}
	}
}

func TestWorkflowDeepDataAndOwnedTranscriptReference(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	// The first file has the exact same bytes and MIME as data, but is not the owning file.
	raw := contract.JSONData(strings.Repeat(`{"x":`, 7000) + "0" + strings.Repeat("}", 7000))
	other := uploadFile(t, s, room, "notes.json", "application/json", string(raw))
	sent, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "deep", Data: raw, Attachments: []contract.Attachment{other}})
	if p != nil {
		t.Fatal(p)
	}
	event, err := s.store.Event(ctx, sent.Seq)
	if err != nil {
		t.Fatal(err)
	}
	post := event.Body.(contract.PostCreated)
	if post.Data != raw || post.DataAttachment == nil || *post.DataAttachment != 1 {
		t.Fatal("deep canonical data or owning index changed")
	}
	item := contract.TranscriptItem{}
	transcriptview.ApplyPost(&item, event)
	if item.Data != "" || item.DataAttachment == nil || *item.DataAttachment != 1 || item.DataRef == nil || item.DataRef.File != post.Attachments[1] || item.DataRef.Summary != "1 keys" {
		t.Fatalf("reference guessed the first same-hash file or lost ownership: %+v", item)
	}
	// Final inputs use the same typed ownership path, without appending data or attachment metadata to their text.
	event.Body = contract.TurnEnded{Data: post.Data, Attachments: post.Attachments, DataAttachment: post.DataAttachment}
	text, _, files, data, index, err := transcriptview.InputBody(event)
	if err != nil || data != raw || index == nil || *index != 1 || len(files) != 2 || strings.Contains(text, "dataAttachment") || strings.Contains(text, string(raw)) {
		t.Fatalf("final transcript input lost exact data/ownership: %v", err)
	}
}
