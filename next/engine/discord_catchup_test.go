package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
