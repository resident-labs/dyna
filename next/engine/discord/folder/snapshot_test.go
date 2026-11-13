package folder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/engine/discord"
)

func TestReadSnapshot(t *testing.T) {
	hits := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path] = true
		switch r.URL.Path {
		case "/api/v10/guilds/1":
			w.Write([]byte(`{"verification_level":2,"default_message_notifications":1}`))
		case "/api/v10/guilds/1/roles":
			w.Write([]byte(`[{"id":"1","name":"@everyone","permissions":"1024"},{"id":"2","name":"Mod","permissions":"8192","position":1,"managed":true}]`))
		case "/api/v10/guilds/1/channels":
			w.Write([]byte(`[{"id":"3","name":"Community","type":4},{"id":"4","name":"general","type":0,"parent_id":"3","permission_overwrites":[{"id":"2","type":0,"allow":"1024","deny":"0"}]},{"id":"5","name":"Voice","type":4},{"id":"6","name":"Lounge","type":2,"parent_id":"5","bitrate":64000,"user_limit":20}]`))
		case "/api/v10/guilds/1/auto-moderation/rules":
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, err := ReadSnapshot(context.Background(), &discord.Client{Endpoint: srv.URL, HTTP: srv.Client(), Token: "secret"}, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 || len(s.Entries) != 4 || s.Entries[1].Parent != "Community" || s.Entries[1].Channel.ID != "4" {
		t.Fatalf("bad snapshot: %+v", s)
	}
	if !s.Entries[2].Category.Sync {
		t.Fatal("empty matching overwrites should sync")
	}
	dir := t.TempDir()
	if _, err := Export(dir, s, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(loaded, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("round trip drift: %+v", plan.Changes)
	}
	b, err := os.ReadFile(filepath.Join(dir, "channels/Community/general/channel.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var ch Channel
	if err := Decode("channel.yaml", b, &ch); err != nil {
		t.Fatal(err)
	}
	if ch.ID != "4" || ch.Allow["Mod"][0] != "view_channel" {
		t.Fatalf("bad channel %+v", ch)
	}
}

func TestExportSafety(t *testing.T) {
	s := Snapshot{Entries: []Entry{{Name: "community", Category: &Category{Identity: Identity{ID: "3"}}}, {Name: "general", Parent: "community", Channel: &Channel{Identity: Identity{ID: "4"}}}}}
	dir := t.TempDir()
	p := filepath.Join(dir, "channels/community")
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "UNMANAGED.md"), []byte("Owned elsewhere\nsecond line"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Export(dir, s, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || !strings.HasSuffix(report[0], "Owned elsewhere") {
		t.Fatal(report)
	}
	if _, err := os.Stat(filepath.Join(p, "category.yaml")); !os.IsNotExist(err) {
		t.Fatal("unmanaged subtree was written")
	}
	if _, err := Export(dir, s, false); err == nil {
		t.Fatal("overwrote without force")
	}
	s.Entries[0].Name = "../escape"
	if _, err := Export(dir, s, true); err == nil {
		t.Fatal("accepted traversal")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(link, Snapshot{}, true); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestPermissionRefusal(t *testing.T) {
	if _, err := permissions("9223372036854775808"); err == nil {
		t.Fatal("unknown bit silently lost")
	}
}

func TestConfigurationReadNotTruncated(t *testing.T) {
	body := `{"name":"` + strings.Repeat("x", 30*1024) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	c := &discord.Client{HTTP: srv.Client(), Endpoint: srv.URL, Token: "secret"}
	r, err := c.ReadConfiguration(context.Background(), "1", "")
	if err != nil || string(r.Body) != body {
		t.Fatalf("truncated: %v", err)
	}
	if _, err := c.ReadConfiguration(context.Background(), "1", "/members"); err == nil {
		t.Fatal("members endpoint accepted")
	}
}
