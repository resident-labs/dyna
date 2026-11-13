package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/room"
	"strings"
)

func TestDiscordPartialChunkRetry(t *testing.T) {
	f := discordFixture(t)
	if err := f.s.store.Write(f.ctx, func(tx *sql.Tx) error {
		for i, text := range []string{"first chunk", "second chunk"} {
			reply := ""
			if i == 0 {
				reply = "300"
			}
			if _, err := tx.ExecContext(f.ctx, `INSERT INTO discord_outbox(room,session,channel,message,text,group_key) VALUES(?,?,?,?,?,?)`, f.room, f.root, "200", reply, text, "reply"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var contents []string
	var references []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content   string          `json:"content"`
			Reference json.RawMessage `json:"message_reference"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		contents = append(contents, body.Content)
		references = append(references, len(body.Reference) > 0)
		n := len(contents)
		mu.Unlock()
		if n == 2 {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	child, cancel := context.WithTimeout(f.ctx, 4500*time.Millisecond)
	defer cancel()
	f.s.discordDrain(child, &discord.Client{HTTP: server.Client(), Endpoint: server.URL, Token: "token"})
	mu.Lock()
	defer mu.Unlock()
	if len(contents) != 3 || contents[0] != "first chunk" || contents[1] != "second chunk" || contents[2] != "second chunk" {
		t.Fatalf("requests: %v", contents)
	}
	if !references[0] || references[1] || references[2] {
		t.Fatalf("reply references: %v", references)
	}
	if _, ok, err := f.s.store.NextDiscordReply(f.ctx); err != nil || ok {
		t.Fatalf("outbox not drained: %v %v", ok, err)
	}
}

func TestDiscordRetrySchedulingAndGroupCancellation(t *testing.T) {
	f := discordFixture(t)
	if err := f.s.store.Write(f.ctx, func(tx *sql.Tx) error {
		for _, group := range []string{"a", "a", "b"} {
			if _, err := tx.ExecContext(f.ctx, `INSERT INTO discord_outbox(room,session,channel,message,text,group_key) VALUES(?,?,?,?,?,?)`, f.room, f.root, "200", "300", "text", group); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, _, err := f.s.store.NextDiscordReply(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.RetryDiscordReply(f.ctx, first.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	next, ok, err := f.s.store.NextDiscordReply(f.ctx)
	if err != nil || !ok || next.Group != "b" {
		t.Fatalf("delayed reply blocked unrelated group: %+v %v", next, err)
	}
	if err := f.s.store.DiscordReplySent(f.ctx, next.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.s.store.NextDiscordReply(f.ctx); err != nil || ok {
		t.Fatalf("tail overtook delayed first chunk: %v %v", ok, err)
	}
	var attempts int
	if err := f.s.store.Read().QueryRowContext(f.ctx, "SELECT attempts FROM discord_outbox WHERE id=?", first.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts %d: %v", attempts, err)
	}
}

func TestDiscordRuntimeClientShared(t *testing.T) {
	f := discordFixture(t)
	a, err := f.s.machineDiscordClient(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.machineDiscordClient(f.ctx)
	if err != nil || a != b {
		t.Fatalf("runtime clients differ: %v", err)
	}
	bot, err := f.s.store.DiscordBot(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.FailDiscordGateway(f.ctx, bot.Credential, "fatal intent failure"); err != nil {
		t.Fatal(err)
	}
	out, problem := f.s.DiscordConfigure(f.ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.DiscordConfigureRequest{})
	if problem != nil || out.GatewayError != "fatal intent failure" {
		t.Fatalf("fatal reason missing: %+v %v", out, problem)
	}
}

func TestDiscordPermanentFailureCancelsTailAndNotifiesOnce(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			f := discordFixture(t)
			if err := f.s.store.Write(f.ctx, func(tx *sql.Tx) error {
				for _, entry := range []struct{ group, text string }{{"a", "bad"}, {"a", "tail"}, {"b", "other"}} {
					if _, err := tx.ExecContext(f.ctx, `INSERT INTO discord_outbox(room,session,channel,message,text,group_key) VALUES(?,?,?,?,?,?)`, f.room, f.root, "200", "300", entry.text, entry.group); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var texts []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Content string `json:"content"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				texts = append(texts, body.Content)
				n := len(texts)
				mu.Unlock()
				if n == 1 {
					w.WriteHeader(status)
					return
				}
				_, _ = w.Write([]byte(`{"id":"999"}`))
			}))
			defer server.Close()
			child, cancel := context.WithTimeout(f.ctx, 2500*time.Millisecond)
			defer cancel()
			f.s.discordDrain(child, &discord.Client{HTTP: server.Client(), Endpoint: server.URL, Token: "token"})
			mu.Lock()
			defer mu.Unlock()
			if len(texts) != 2 || texts[0] != "bad" || texts[1] != "other" {
				t.Fatalf("requests %v", texts)
			}
			var notices int
			if err := f.s.store.Read().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM events WHERE room=? AND type='session.notice'", f.room).Scan(&notices); err != nil || notices != 1 {
				t.Fatalf("notices %d: %v", notices, err)
			}
		})
	}
}

func TestDiscordOwnerRichContentIsOutsideAndInputIsDeduplicated(t *testing.T) {
	f := discordFixture(t)
	var m discord.Message
	if err := json.Unmarshal([]byte(`{"id":"700","channel_id":"200","guild_id":"100","content":"owner words","author":{"id":"1","username":"owner"},"embeds":[{"title":"untrusted preview"}],"sticker_items":[{"name":"untrusted sticker"}],"poll":{"question":{"text":"untrusted poll"}}}`), &m); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.s.discordInput(f.ctx, f.client, m); err != nil {
			t.Fatal(err)
		}
	}
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatalf("duplicate events %d: %v", len(events), err)
	}
	b := events[0].Body.(contract.DiscordMessage)
	if b.Text != "owner words" || !strings.Contains(b.RichContent, "untrusted preview") {
		t.Fatalf("words and rich content mixed: %+v", b)
	}
	rendered, err := room.RenderDelivery(b)
	if err != nil {
		t.Fatal(err)
	}
	pieces := strings.Split(rendered, "</outside>")
	if len(pieces) != 2 || !strings.Contains(pieces[0], "untrusted preview") || !strings.Contains(pieces[0], "untrusted sticker") || !strings.Contains(pieces[0], "untrusted poll") || !strings.Contains(pieces[1], "owner words") {
		t.Fatalf("trust boundary lost: %s", rendered)
	}
	if strings.ContainsAny(frameField("Admin\r\n[Dyna]"), "\r\n") {
		t.Fatal("header permits line breaks")
	}
	f.s.discordRuntime.Lock()
	expiry := f.s.discordTyping["200"]
	f.s.discordRuntime.Unlock()
	if time.Until(expiry) < 9*time.Minute {
		t.Fatal("input did not enable typing")
	}
}
