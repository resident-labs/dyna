package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
)

func TestDiscordCatchUpWindowAndDedupe(t *testing.T) {
	f := discordFixture(t)
	snowflake := func(t time.Time) string { return strconv.FormatUint(uint64(t.UnixMilli()-1420070400000)<<22, 10) }
	recent := snowflake(time.Now().Add(-time.Minute))
	old := snowflake(time.Now().Add(-11 * time.Minute))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/members/") {
			fmt.Fprint(w, `{"roles":["10"]}`)
			return
		}
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		// Include an old message to verify the intake window independently of REST.
		fmt.Fprintf(w, `[{"id":%q,"author":{"id":"2","username":"ari"},"content":"missed"},{"id":%q,"author":{"id":"2"}}]`, recent, old)
	}))
	defer srv.Close()
	client := &discord.Client{HTTP: srv.Client(), Endpoint: srv.URL, BotID: "900", Token: "test-secret"}
	for i := 0; i < 2; i++ {
		count, err := f.s.discordCatchUpChannel(f.ctx, client, "100", "200", func(ctx context.Context, m discord.Message) error { return f.s.discordInput(ctx, client, m) })
		if err != nil || count != 1 {
			t.Fatalf("catch-up %d: %d %v", i, count, err)
		}
	}
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: %d %v", len(events), err)
	}
}

func TestCatchUpBlockedHistoryAndNewLiveMessageThenRestart(t *testing.T) {
	f := discordFixture(t)
	snowflake := func(when time.Time) string { return strconv.FormatUint(uint64(when.UnixMilli()-1420070400000)<<22, 10) }
	older, newer := snowflake(time.Now().Add(-time.Minute)), snowflake(time.Now())
	blocked, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/members/") {
			fmt.Fprint(w, `{"roles":["10"]}`)
			return
		}
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		if requests.Add(1) == 1 {
			close(blocked)
			<-release
		}
		fmt.Fprintf(w, `[{"id":%q,"author":{"id":"1","username":"owner"}},{"id":%q,"author":{"id":"1","username":"owner"}}]`, newer, older)
	}))
	defer srv.Close()
	client := &discord.Client{HTTP: srv.Client(), Endpoint: srv.URL, Token: "test-secret", BotID: "900"}
	receive := func(ctx context.Context, m discord.Message) error { return f.s.discordInput(ctx, client, m) }
	done := make(chan error, 1)
	go func() { _, err := f.s.discordCatchUpChannel(f.ctx, client, "100", "200", receive); done <- err }()
	<-blocked
	var live discord.Message
	live.ID, live.Guild, live.Channel, live.Author.ID = newer, "100", "200", "1"
	err := receive(f.ctx, live)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A new process has no cursor: it repeats the whole bounded window, even
	// though the newer live message arrived while the first history was blocked.
	if _, err := f.s.discordCatchUpChannel(f.ctx, client, "100", "200", receive); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %d %v", len(events), err)
	}
}

func TestDiscordCatchUpOversizedHistory(t *testing.T) {
	f := discordFixture(t)
	var messages []discord.Message
	for i := 0; i < 30; i++ {
		var m discord.Message
		m.ID = strconv.FormatUint(uint64(time.Now().Add(-time.Minute).UnixMilli()-1420070400000)<<22|uint64(i), 10)
		m.Author.ID = "1"
		m.Content = strings.Repeat("a", 1000)
		messages = append(messages, m)
	}
	body, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 20*1024 {
		t.Fatal("fixture too small")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/members/") {
			fmt.Fprint(w, `{"roles":["10"]}`)
			return
		}
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	client := &discord.Client{HTTP: srv.Client(), Endpoint: srv.URL, Token: "test-secret", BotID: "900"}
	count, err := f.s.discordCatchUpChannel(f.ctx, client, "100", "200", func(ctx context.Context, m discord.Message) error { return f.s.discordInput(ctx, client, m) })
	if err != nil || count != 30 {
		t.Fatalf("recovered %d: %v", count, err)
	}
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 30 {
		t.Fatalf("events %d: %v", len(events), err)
	}
}
