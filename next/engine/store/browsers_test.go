package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/contract"
)

func TestBrowserProfilesAndRooms(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	roomA := contract.RoomID("room_a")
	roomB := contract.RoomID("room_b")
	if err := s.CreateRoom(ctx, roomA, "alpha", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(ctx, roomB, "beta", now); err != nil {
		t.Fatal(err)
	}

	if err := s.BrowserCreate(ctx, "personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.BrowserCreate(ctx, "personal"); !errors.Is(err, ErrBrowserExists) {
		t.Fatalf("duplicate create = %v, want ErrBrowserExists", err)
	}

	// Room without profile returns empty.
	if prof, others, err := s.RoomBrowser(ctx, roomA); err != nil || prof != "" || len(others) != 0 {
		t.Fatalf("no-profile RoomBrowser: prof=%q others=%v err=%v", prof, others, err)
	}

	// Assign to roomA, then roomB.
	if err := s.SetRoomBrowser(ctx, roomA, "personal"); err != nil {
		t.Fatal(err)
	}
	prof, others, err := s.RoomBrowser(ctx, roomA)
	if err != nil || prof != "personal" || len(others) != 0 {
		t.Fatalf("RoomBrowser alone: prof=%q others=%v err=%v", prof, others, err)
	}
	if err := s.SetRoomBrowser(ctx, roomB, "personal"); err != nil {
		t.Fatal(err)
	}
	prof, others, err = s.RoomBrowser(ctx, roomA)
	if err != nil || prof != "personal" || !slices.Equal(others, []string{"beta"}) {
		t.Fatalf("RoomBrowser shared: prof=%q others=%v err=%v", prof, others, err)
	}

	// BrowserList lists rooms.
	list, err := s.BrowserList(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "personal" || !slices.Equal(list[0].Rooms, []string{"alpha", "beta"}) {
		t.Fatalf("BrowserList: %+v %v", list, err)
	}

	// Remove refused while rooms reference it.
	if err := s.BrowserRemove(ctx, "personal"); !errors.Is(err, ErrBrowserInUse) {
		t.Fatalf("remove while referenced = %v, want ErrBrowserInUse", err)
	}

	// Unassign rooms and remove profile.
	if err := s.SetRoomBrowser(ctx, roomA, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomBrowser(ctx, roomB, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BrowserRemove(ctx, "personal"); err != nil {
		t.Fatalf("BrowserRemove: %v", err)
	}
	if err := s.BrowserRemove(ctx, "personal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove = %v, want ErrNotFound", err)
	}
}

func TestBrowserLeasesMigrationDropsTableAndKeepsProfiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Downgrade to schema 127 (v1 browsers + browser_leases with active lease).
	_, err = s.writer.Exec(`
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes,browser) VALUES('room_life','life',0,0,0,'personal');
		ALTER TABLE browsers ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
		INSERT INTO browsers(name,generation) VALUES('personal',4);
		CREATE TABLE browser_leases (profile TEXT PRIMARY KEY REFERENCES browsers(name) ON DELETE CASCADE, box TEXT NOT NULL, session TEXT NOT NULL, generation INTEGER NOT NULL, since INTEGER NOT NULL);
		INSERT INTO browser_leases(profile,box,session,generation,since) VALUES('personal','box_1','ses_1',4,1700000000000);
		DROP TABLE service_feed_messages;
		DROP TABLE service_outbox;
		ALTER TABLE services DROP COLUMN feed_in;
		ALTER TABLE services DROP COLUMN feed_out;
		ALTER TABLE services DROP COLUMN feed_last_at; DROP TABLE agy_streak; ALTER TABLE sessions DROP COLUMN handoff; ALTER TABLE sessions DROP COLUMN agy_account; ALTER TABLE sessions DROP COLUMN cleared_at; ALTER TABLE turns DROP COLUMN cleared_at;
		UPDATE meta SET value='127' WHERE key='schema_version';
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("upgrade 127->128: %v", err)
	}
	defer s.Close()

	// browser_leases must be gone.
	var count int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='browser_leases'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("browser_leases still exists: count=%d err=%v", count, err)
	}
	// browsers.generation must be gone, while personal and room assignment survive.
	if _, err := s.Read().Exec(`SELECT generation FROM browsers`); err == nil {
		t.Fatal("browsers.generation column still exists")
	}
	prof, _, err := s.RoomBrowser(context.Background(), "room_life")
	if err != nil || prof != "personal" {
		t.Fatalf("RoomBrowser after migration: prof=%q err=%v", prof, err)
	}
	list, err := s.BrowserList(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "personal" || !slices.Equal(list[0].Rooms, []string{"life"}) {
		t.Fatalf("BrowserList after migration: %+v %v", list, err)
	}
}

func TestReadV1ChromiumCookiesRealProfile(t *testing.T) {
	bin, err := exec.LookPath("chromium")
	if err != nil {
		t.Skip("chromium not installed")
	}

	startHeadless := func(profileDir string) (context.Context, *websocket.Conn, func()) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		var stderr bytes.Buffer
		cmd := exec.Command(bin,
			"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
			"--no-first-run", "--no-default-browser-check", "--password-store=basic",
			"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
			"--user-data-dir="+profileDir, "about:blank",
		)
		cmd.Stderr = &stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()

		var wsURL string
		portFile := filepath.Join(profileDir, "DevToolsActivePort")
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			if data, err := os.ReadFile(portFile); err == nil {
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) >= 2 {
					p, err := strconv.Atoi(strings.TrimSpace(lines[0]))
					if err == nil && p > 0 {
						wsURL = fmt.Sprintf("ws://127.0.0.1:%d%s", p, strings.TrimSpace(lines[1]))
						break
					}
				}
			}
			select {
			case <-done:
				cancel()
				t.Fatalf("chromium exited before writing DevToolsActivePort: %s", stderr.String())
			case <-time.After(50 * time.Millisecond):
			}
		}
		if wsURL == "" {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
			cancel()
			t.Fatalf("chromium did not write DevToolsActivePort: %s", stderr.String())
		}
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
			cancel()
			t.Fatal(err)
		}
		conn.SetReadLimit(64 << 20)
		stop := func() {
			req, _ := json.Marshal(map[string]any{"id": 999, "method": "Browser.close"})
			_ = conn.Write(ctx, websocket.MessageText, req)
			_ = conn.Close(websocket.StatusNormalClosure, "")
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}
			cancel()
		}
		return ctx, conn, stop
	}

	cdpCall := func(ctx context.Context, conn *websocket.Conn, id int, method string, params any) json.RawMessage {
		p, _ := json.Marshal(params)
		req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(p)})
		if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
			t.Fatal(err)
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var resp struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  any             `json:"error"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				t.Fatal(err)
			}
			if resp.ID != id {
				continue
			}
			if resp.Error != nil {
				t.Fatalf("%s error: %v", method, resp.Error)
			}
			return resp.Result
		}
	}

	// 1. Seed a real v1 Chromium profile on disk.
	v1Dir := t.TempDir()
	ctx1, conn1, stop1 := startHeadless(v1Dir)
	future := float64(time.Now().Add(48 * time.Hour).Unix())
	seed := []map[string]any{
		{"name": "__Host-auth", "value": "real-host-val", "domain": "app.example.com", "path": "/", "secure": true, "httpOnly": true, "sameSite": "Lax", "expires": future},
		{"name": "strict_c", "value": "real-strict-val", "domain": ".example.com", "path": "/a", "secure": true, "httpOnly": false, "sameSite": "Strict", "expires": future},
		{"name": "part_false", "value": "real-part-false", "domain": ".sub.example.org", "path": "/p", "secure": true, "sameSite": "None", "expires": future, "partitionKey": map[string]any{"topLevelSite": "https://example.net", "hasCrossSiteAncestor": false}},
		{"name": "part_true", "value": "real-part-true", "domain": "embed.example.org", "path": "/", "secure": true, "sameSite": "None", "expires": future, "partitionKey": map[string]any{"topLevelSite": "https://example.net", "hasCrossSiteAncestor": true}},
		{"name": "unspec_ss", "value": "real-unspec", "domain": "plain.example.com", "path": "/", "secure": false, "httpOnly": true, "expires": future},
		{"name": "empty_val", "value": "", "domain": "plain.example.com", "path": "/", "expires": future},
	}
	cdpCall(ctx1, conn1, 1, "Storage.setCookies", map[string]any{"cookies": seed})
	stop1()

	dbPath, ok := browser.FindV1CookiesDB(v1Dir)
	if !ok {
		t.Fatal("chromium did not create Default/Cookies")
	}

	// 2. Read the SQLite file directly (without Chromium) and decrypt every cookie.
	v1DB, err := ReadV1ChromiumCookies(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("ReadV1ChromiumCookies: %v", err)
	}
	if v1DB.Version < 24 {
		t.Fatalf("expected Chromium DB meta version >= 24, got %d", v1DB.Version)
	}
	// Verify our encryptor produces byte-identical ciphertext to Chromium's real row.
	for _, row := range v1DB.Rows {
		if row.Name == "__Host-auth" {
			reEnc, err := browser.EncryptV1CookieValue(row.HostKey, "real-host-val", v1DB.Version)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(reEnc, row.EncryptedValue) {
				t.Fatalf("EncryptV1CookieValue bytes differ from Chromium's encrypted_value")
			}
		}
	}

	cookies, err := browser.DecodeV1Cookies(v1DB)
	if err != nil {
		t.Fatalf("DecodeV1Cookies: %v", err)
	}
	if len(cookies) != len(seed) {
		t.Fatalf("decoded %d cookies, want %d", len(cookies), len(seed))
	}

	// 3. Load the migrated cookies into a brand-new Chromium profile via Storage.setCookies
	// and verify every name, value, attribute, and partitionKey round-trips verbatim.
	freshDir := t.TempDir()
	ctx2, conn2, stop2 := startHeadless(freshDir)
	defer stop2()
	cdpCall(ctx2, conn2, 1, "Storage.setCookies", map[string]any{"cookies": cookies})
	rawGot := cdpCall(ctx2, conn2, 2, "Storage.getCookies", map[string]any{})
	var gotPayload struct {
		Cookies []map[string]any `json:"cookies"`
	}
	if err := json.Unmarshal(rawGot, &gotPayload); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, c := range gotPayload.Cookies {
		byName[c["name"].(string)] = c
	}
	wantVals := map[string]string{
		"__Host-auth": "real-host-val",
		"strict_c":    "real-strict-val",
		"part_false":  "real-part-false",
		"part_true":   "real-part-true",
		"unspec_ss":   "real-unspec",
		"empty_val":   "",
	}
	for name, wantVal := range wantVals {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("cookie %q missing after round-trip into fresh Chromium", name)
		}
		if c["value"] != wantVal {
			t.Fatalf("cookie %q value = %q, want %q", name, c["value"], wantVal)
		}
	}
	if byName["__Host-auth"]["sameSite"] != "Lax" || byName["__Host-auth"]["httpOnly"] != true || byName["__Host-auth"]["secure"] != true {
		t.Fatalf("unexpected __Host-auth attributes: %v", byName["__Host-auth"])
	}
	if byName["strict_c"]["sameSite"] != "Strict" {
		t.Fatalf("unexpected strict_c sameSite: %v", byName["strict_c"])
	}
	if _, hasSS := byName["unspec_ss"]["sameSite"]; hasSS {
		t.Fatalf("unexpected sameSite on unspec_ss: %v", byName["unspec_ss"])
	}
	pkF, _ := byName["part_false"]["partitionKey"].(map[string]any)
	if pkF == nil || pkF["topLevelSite"] != "https://example.net" || pkF["hasCrossSiteAncestor"] != false {
		t.Fatalf("unexpected part_false partitionKey: %v", byName["part_false"]["partitionKey"])
	}
	pkT, _ := byName["part_true"]["partitionKey"].(map[string]any)
	if pkT == nil || pkT["topLevelSite"] != "https://example.net" || pkT["hasCrossSiteAncestor"] != true {
		t.Fatalf("unexpected part_true partitionKey: %v", byName["part_true"]["partitionKey"])
	}
}
