package roomsvc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func (e *env) repoRoom(name string) room.Record {
	e.t.Helper()
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: name, Repo: e.t.TempDir(), CreatedBy: "test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) posts(roomID string) []room.Message {
	e.t.Helper()
	msgs, err := e.svc.Notify.Rooms.Messages(roomID)
	if err != nil {
		e.t.Fatal(err)
	}
	return msgs
}

func TestSuggestName(t *testing.T) {
	for _, c := range []struct{ task, want string }{
		{"Find why the decoder rejects truncated input", "decoder-rejects"},
		{"parser", "parser"},
		{"please fix it", ""}, // only filler words
		{"fix foo_bar.go panic", "foobargo-panic"},
		{"Review the CRC-32, then report", "crc-32-report"},
		{"team", ""}, // reserved for room mentions
		{"root", ""}, // reserved
		{"ünïcödé ✨ décodeur", "ncd-dcodeur"},
		{"✨ 🎉", ""},
		{"look at /home/user/x and @lead", ""},
		{"42 widgets", ""}, // must start with a letter
		{"supercalifragilisticexpialidocious antidisestablishmentarianism", "supercalifragilisticexpialidocio"},
	} {
		if got := SuggestName(c.task); got != c.want {
			t.Errorf("SuggestName(%q) = %q, want %q", c.task, got, c.want)
		}
	}
}

func TestSplitSpawn(t *testing.T) {
	for _, c := range []struct {
		draft, prof, task string
		ok                bool
	}{
		{"/mock fix the parser", "mock", "fix the parser", true},
		{"  /mock\nmulti\nline", "mock", "multi\nline", true},
		{"/mock", "mock", "", true},
		{"/", "", "", true}, // a command being typed: never posts "/"
		{" / ", "", "", true},
		{"/ hello", "", "", false},
		{"/opuss typo", "opuss", "typo", true}, // a spawn attempt: it must error, not post
		{"//mock hi", "", "", false},
		{"/home/user/x is broken", "", "", false},
		{"/9lives", "", "", false},
		{"@lead /mock hi", "", "", false},
		{"plain text", "", "", false},
	} {
		prof, task, ok := splitSpawn(c.draft)
		if prof != c.prof || task != c.task || ok != c.ok {
			t.Errorf("splitSpawn(%q) = %q, %q, %v; want %q, %q, %v", c.draft, prof, task, ok, c.prof, c.task, c.ok)
		}
	}
	if got := UnescapeSlash("//mock hi"); got != "/mock hi" {
		t.Errorf("UnescapeSlash = %q", got)
	}
}

// TestSendNeverMispostsSpawns proves the router: a spawn-shaped draft never
// becomes a post, a "//" draft never starts a session, and a path posts.
func TestSendNeverMispostsSpawns(t *testing.T) {
	e := newEnv(t)
	rec := e.repoRoom("alpha")
	lead := e.start(rec, "lead", nil)
	before := len(e.posts(rec.ID))

	for _, draft := range []string{"/opuss fix it", "/mock", "/mock   ", "/"} {
		if _, err := e.svc.Send(e.ctx, "alpha", draft, "", ""); err == nil {
			t.Fatalf("Send(%q) succeeded; want an error", draft)
		}
		if _, err := e.svc.Post(e.ctx, "alpha", draft, ""); err == nil {
			t.Fatalf("Post(%q) succeeded; a spawn-shaped draft must never post", draft)
		}
	}
	if _, err := e.svc.Send(e.ctx, "alpha", "/mock with a file", "", "", room.Attachment{ID: "x"}); err == nil || !strings.Contains(err.Error(), "attachment") {
		t.Fatalf("spawn with an invalid attachment: err = %v", err)
	}
	if _, err := e.svc.Send(e.ctx, "alpha", "/mock as a reply", "msg_1", ""); err == nil || !strings.Contains(err.Error(), "reply") {
		t.Fatalf("spawn as reply: err = %v", err)
	}
	if got := len(e.posts(rec.ID)); got != before {
		t.Fatalf("refused spawns wrote %d posts", got-before)
	}
	members, _ := e.svc.Members("alpha", true)
	if len(members) != 2 {
		t.Fatalf("refused spawns started sessions: %d members", len(members))
	}

	res, err := e.svc.Send(e.ctx, "alpha", "//mock is a command", "", "")
	if err != nil || res.Kind != "post" || res.Post.Message.Text != "/mock is a command" {
		t.Fatalf("escaped send = %+v, %v", res, err)
	}
	e.wait(lead.ID, 0) // the post woke lead; let that turn end
	res, err = e.svc.Send(e.ctx, "alpha", "/home/user/x is broken", "", "")
	if err != nil || res.Kind != "post" || res.Post.Message.Text != "/home/user/x is broken" {
		t.Fatalf("path send = %+v, %v", res, err)
	}
	e.wait(lead.ID, 0)
	// A post to @root starts the internal root, but never a spawn.
	spawned := 0
	members, _ = e.svc.Members("alpha", true)
	for _, m := range members {
		if m.Role != subagent.RoleRoot {
			spawned++
		}
	}
	if spawned != 2 {
		t.Fatalf("posts started sessions: %d members", spawned)
	}
}

func TestPreviewSpawn(t *testing.T) {
	e := newEnv(t)
	rec := e.repoRoom("alpha")
	d, err := e.svc.Preview("alpha", "/mock Find why the decoder rejects input")
	if err != nil {
		t.Fatal(err)
	}
	sp := d.Spawn
	if sp == nil || sp.Error != "" || sp.Name != "decoder-rejects" || sp.Dir != rec.Repo || sp.Room != "alpha" || sp.Harness != profile.HarnessMock {
		t.Fatalf("spawn preview = %+v", sp)
	}
	if d, _ := e.svc.Preview("alpha", "/opuss fix"); d.Spawn == nil || !strings.Contains(d.Spawn.Error, "opuss") {
		t.Fatalf("typo preview = %+v", d.Spawn)
	}
	if d, _ := e.svc.Preview("alpha", "//mock hi"); d.Spawn != nil || d.Text != "/mock hi" {
		t.Fatalf("escaped preview = %+v", d)
	}
	e.room("bare")
	if d, _ := e.svc.Preview("bare", "/mock fix it"); d.Spawn == nil || !strings.Contains(d.Spawn.Error, "no repo") {
		t.Fatalf("no-repo preview = %+v", d.Spawn)
	}
	if _, err := e.svc.Send(e.ctx, "bare", "/mock fix it", "", ""); err == nil {
		t.Fatal("a room without a repo must not spawn")
	}
}

// TestSpawnRecordFailureIsNotFatal: once Start succeeds the spawn succeeds,
// so the composer clears and Enter cannot start a twin.
func TestSpawnRecordFailureIsNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	e := newEnv(t)
	rec := e.repoRoom("alpha")
	path, err := e.svc.Notify.Rooms.FilesDir(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	log := strings.TrimSuffix(path, "files") + "messages.jsonl"
	if err := os.Chmod(log, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(log, 0o600) })
	res, err := e.svc.StartSession(e.ctx, "alpha", "/mock RESPOND: ok")
	if err != nil || res.Warning == "" || res.Member.ID == "" {
		t.Fatalf("StartSession = %+v, %v; want success with a warning", res, err)
	}
	e.wait(res.Member.ID, 1)
}

func TestProfilesPayloadHasNoSecrets(t *testing.T) {
	e := newEnv(t)
	store := &profile.Store{Profiles: []profile.Profile{
		{Name: "mock", Harness: profile.HarnessMock, Default: true, Env: map[string]string{"API_KEY": "secret"}, ExtraArgs: []string{"--key=secret"}},
		{Name: "off", Harness: profile.HarnessMock, Disabled: true},
		{Name: "solo", Harness: profile.HarnessMock, Uses: []string{profile.UseAgents}},
		{Name: "svc", Harness: profile.HarnessMock, Uses: []string{profile.UseServices}},
	}}
	e.mgr.Profiles = func() (*profile.Store, error) { return store, nil }
	got, err := e.svc.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "mock" || !got[0].Default || got[1].Name != "solo" {
		t.Fatalf("profiles = %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "secret") || strings.Contains(strings.ToLower(string(b)), "env") {
		t.Fatalf("payload leaks profile config: %s", b)
	}
}

func TestClipIsRuneSafe(t *testing.T) {
	long := strings.Repeat("é", 300)
	got := clip(long, 3, 280)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 281 || !json.Valid([]byte(`"`+got+`"`)) {
		t.Fatalf("clip = %d runes", len([]rune(got)))
	}
	if got := clip("a\nb\nc\nd", 3, 280); got != "a\nb\nc…" {
		t.Fatalf("clip lines = %q", got)
	}
}
