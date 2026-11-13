package folder

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
)

func TestFakeDiscordConfigurationAcceptance(t *testing.T) {
	fake := fakediscord.New(t)
	fake.SetConfiguration(fakediscord.CommunityConfiguration())
	client := &discord.Client{Endpoint: fake.HTTP.URL, HTTP: fake.HTTP.Client(), Token: fake.Token}
	live, err := ReadSnapshot(context.Background(), client, "100")
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]Entry{}
	for _, e := range live.Entries {
		entries[e.Path] = e
	}
	if !entries["Community"].Category.Sync || entries["Moderation"].Category.Sync {
		t.Fatal("sync detection incorrect")
	}
	if entries["Community/help"].Channel.Tags[1] != "bug" || entries["Community/announcements"].Channel.Type != "announcement" {
		t.Fatal("forum/announcement configuration missing")
	}
	if entries["Moderation/mod-log"].Channel.Allow["member:12345"][0] != "view_channel" {
		t.Fatal("member overwrite missing")
	}
	dir := t.TempDir()
	external := filepath.Join(dir, "channels/external")
	if err := os.MkdirAll(external, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "UNMANAGED.md"), []byte("Webhook mirrors; external owner\nDetails here."), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Export(dir, live, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || !strings.HasSuffix(report[0], "Webhook mirrors; external owner") {
		t.Fatal(report)
	}
	if _, err := os.Stat(filepath.Join(external, "mirror")); !os.IsNotExist(err) {
		t.Fatal("export entered unmanaged subtree")
	}
	desired, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(desired, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("round trip drift: %+v", plan.Changes)
	}
	requests := fake.RequestLog()
	if len(requests) != 4 {
		t.Fatalf("unexpected requests: %+v", requests)
	}
	allowed := map[string]bool{"/api/v10/guilds/100": true, "/api/v10/guilds/100/roles": true, "/api/v10/guilds/100/channels": true, "/api/v10/guilds/100/auto-moderation/rules": true}
	for _, r := range requests {
		if r.Method != http.MethodGet || !allowed[r.Path] {
			t.Fatalf("unexpected request: %+v", r)
		}
		delete(allowed, r.Path)
	}
	if len(allowed) != 0 || len(fake.Writes()) != 0 || fake.HistoryCalls() != 0 {
		t.Fatal("snapshot omitted configuration or read messages or wrote Discord")
	}
}

func TestFakeDiscordRecordsWrites(t *testing.T) {
	fake := fakediscord.New(t)
	client := &discord.Client{Endpoint: fake.HTTP.URL, HTTP: fake.HTTP.Client(), Token: fake.Token}
	_, err := client.API(context.Background(), "PATCH", "/api/v10/channels/200", []byte(`{"topic":"test"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	writes := fake.Writes()
	if len(writes) != 1 || writes[0].Method != "PATCH" || writes[0].Path != "/api/v10/channels/200" || writes[0].Body != `{"topic":"test"}` {
		t.Fatalf("write not recorded: %+v", writes)
	}
}
