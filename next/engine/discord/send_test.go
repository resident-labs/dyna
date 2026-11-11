package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendsAllowOnlyUserMentions(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []byte
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			data = []byte(r.FormValue("payload_json"))
		} else {
			var body json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			data = body
		}
		var payload struct {
			Content  string `json:"content"`
			Mentions struct {
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
		}
		if json.Unmarshal(data, &payload) != nil || len(payload.Mentions.Parse) != 1 || payload.Mentions.Parse[0] != "users" {
			t.Errorf("unsafe mentions %s", data)
		}
		if !strings.Contains(payload.Content, "@everyone") {
			t.Errorf("text was rewritten %s", data)
		}
		calls++
		w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), Endpoint: server.URL, Token: "secret"}
	text := "@everyone @here <@&123> <@456>"
	if _, _, err := client.Send(context.Background(), "200", text, "300"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendFiles(context.Background(), "200", text, "300", []File{{Name: "a.txt", Data: []byte("hello")}}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("got %d sends", calls)
	}
}
