package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/contract"
)

func testCookie(t *testing.T, obj map[string]any) browser.Cookie {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	c, err := browser.ParseCookie(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBrowserSyncConcurrentContextAndCounts(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	dataDir := t.TempDir()
	r.s.SetLayout(box.Layout{Data: dataDir})

	// Non-owner cannot manage browser profiles.
	if _, problem := r.s.BrowserCreate(ctx, r.root, contract.BrowserCreateRequest{Name: "personal"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("non-owner create: %+v", problem)
	}
	if _, problem := r.s.BrowserList(ctx, r.worker, contract.BrowserListRequest{}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("non-owner list: %+v", problem)
	}

	// Before assigning a profile, sync returns 204 No Content.
	hA := r.s.BrowserSync(r.worker, r.room)
	hB := r.s.BrowserSync(r.root, r.room)
	rec0 := httptest.NewRecorder()
	hA.ServeHTTP(rec0, httptest.NewRequest(http.MethodGet, "/v1/browser/sync", nil))
	if rec0.Code != http.StatusNoContent {
		t.Fatalf("no-profile GET status = %d, want 204", rec0.Code)
	}

	// Owner creates profile and assigns it to r.room.
	if _, problem := r.s.BrowserCreate(ctx, r.owner, contract.BrowserCreateRequest{Name: "personal"}); problem != nil {
		t.Fatal(problem)
	}
	profileName := "personal"
	if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, Browser: &profileName}); problem != nil {
		t.Fatal(problem)
	}

	roomSection := func(who contract.Principal) string {
		res, problem := r.s.ContextSnapshot(ctx, who, contract.ContextSnapshotRequest{Section: contract.ContextRoom})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, sec := range res.Sections {
			if sec.Key == contract.ContextRoom {
				return sec.Text
			}
		}
		return ""
	}
	if got := roomSection(r.worker); got != "Browser: personal" {
		t.Fatalf("dyna_room alone = %q", got)
	}

	// Assign same profile to a second room; dyna_room lists sibling without exclusivity text.
	room2, problem := r.s.RoomCreate(ctx, r.owner, contract.RoomCreateRequest{Name: "ops", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: room2.Room, Browser: &profileName}); problem != nil {
		t.Fatal(problem)
	}
	if got := roomSection(r.worker); got != "Browser: personal, shared with ops" {
		t.Fatalf("dyna_room shared = %q", got)
	}

	// Both Box A and Box B pull at once with no exclusivity.
	recPullA := httptest.NewRecorder()
	hA.ServeHTTP(recPullA, httptest.NewRequest(http.MethodGet, "/v1/browser/sync", nil))
	if recPullA.Code != http.StatusOK {
		t.Fatalf("pull A status = %d", recPullA.Code)
	}
	recPullB := httptest.NewRecorder()
	hB.ServeHTTP(recPullB, httptest.NewRequest(http.MethodGet, "/v1/browser/sync", nil))
	if recPullB.Code != http.StatusOK {
		t.Fatalf("pull B status = %d", recPullB.Code)
	}

	// Box A pushes cookies (including a secret token value).
	const secretVal = "LIVE_EMAIL_SESSION_SECRET_XYZ"
	cHost := testCookie(t, map[string]any{
		"name": "__Host-auth", "value": secretVal, "domain": "mail.example.com", "path": "/", "secure": true, "httpOnly": true, "sameSite": "Lax", "expires": -1, "session": true,
	})
	cPart := testCookie(t, map[string]any{
		"name": "part", "value": "p1", "domain": ".shop.example.org", "path": "/", "secure": true, "sameSite": "None", "expires": -1, "session": true,
		"partitionKey": map[string]any{"topLevelSite": "https://example.net", "hasCrossSiteAncestor": false},
	})
	pushBody, _ := json.Marshal(browser.SyncRequest{
		Profile:     "personal",
		BaseVersion: 0,
		Upserts:     []browser.Cookie{cHost, cPart},
	})
	recPushA := httptest.NewRecorder()
	hA.ServeHTTP(recPushA, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(pushBody)))
	if recPushA.Code != http.StatusOK {
		t.Fatalf("push A status = %d", recPushA.Code)
	}
	var respA browser.SyncResponse
	if err := json.Unmarshal(recPushA.Body.Bytes(), &respA); err != nil {
		t.Fatal(err)
	}
	if respA.Profile != "personal" || respA.Version != 1 || len(respA.Upserts) != 0 {
		t.Fatalf("push A resp: profile=%q ver=%d upserts=%d", respA.Profile, respA.Version, len(respA.Upserts))
	}

	// Verify jar.json written with mode 0600 on disk.
	jarPath := filepath.Join(dataDir, "browsers", "personal", browser.JarFilename)
	info, err := os.Stat(jarPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("jar.json stat: err=%v info=%v", err, info)
	}

	// Box B syncs from version 0 and receives both cookies.
	syncBBody, _ := json.Marshal(browser.SyncRequest{Profile: "personal", BaseVersion: 0})
	recSyncB := httptest.NewRecorder()
	hB.ServeHTTP(recSyncB, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(syncBBody)))
	if recSyncB.Code != http.StatusOK {
		t.Fatalf("sync B status = %d", recSyncB.Code)
	}
	var respB browser.SyncResponse
	if err := json.Unmarshal(recSyncB.Body.Bytes(), &respB); err != nil {
		t.Fatal(err)
	}
	if respB.Version != 1 || len(respB.Upserts) != 2 {
		t.Fatalf("sync B resp: ver=%d upserts=%d", respB.Version, len(respB.Upserts))
	}

	// Box B deletes cPart -> Box A receives tombstone on next sync.
	delBody, _ := json.Marshal(browser.SyncRequest{Profile: "personal", BaseVersion: 1, Deletes: []browser.CookieKey{cPart.Key}})
	recDelB := httptest.NewRecorder()
	hB.ServeHTTP(recDelB, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(delBody)))
	if recDelB.Code != http.StatusOK {
		t.Fatalf("del B status = %d", recDelB.Code)
	}
	pullA2Body, _ := json.Marshal(browser.SyncRequest{Profile: "personal", BaseVersion: 1})
	recPullA2 := httptest.NewRecorder()
	hA.ServeHTTP(recPullA2, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(pullA2Body)))
	if recPullA2.Code != http.StatusOK {
		t.Fatalf("pull A2 status = %d", recPullA2.Code)
	}
	var respA2 browser.SyncResponse
	if err := json.Unmarshal(recPullA2.Body.Bytes(), &respA2); err != nil {
		t.Fatal(err)
	}
	if respA2.Version != 2 || len(respA2.Deletes) != 1 || respA2.Deletes[0].ID() != cPart.Key.ID() {
		t.Fatalf("pull A2 resp: ver=%d deletes=%d", respA2.Version, len(respA2.Deletes))
	}

	// BrowserList returns counts only and never contains cookie values.
	listRes, problem := r.s.BrowserList(ctx, r.owner, contract.BrowserListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	listJSON, _ := json.Marshal(listRes)
	if strings.Contains(string(listJSON), secretVal) {
		t.Fatal("BrowserList response leaked a cookie value")
	}
	if len(listRes.Browsers) != 1 || listRes.Browsers[0].Cookies != 1 || listRes.Browsers[0].Version != 2 {
		t.Fatalf("BrowserList = %+v", listRes.Browsers)
	}

	// Switching the room to a different profile ignores stale pushes from personal and returns work's jar.
	if _, problem := r.s.BrowserCreate(ctx, r.owner, contract.BrowserCreateRequest{Name: "work"}); problem != nil {
		t.Fatal(problem)
	}
	workName := "work"
	if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, Browser: &workName}); problem != nil {
		t.Fatal(problem)
	}
	stalePush, _ := json.Marshal(browser.SyncRequest{Profile: "personal", BaseVersion: 2, Deletes: []browser.CookieKey{cHost.Key}})
	recSwitch := httptest.NewRecorder()
	hA.ServeHTTP(recSwitch, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(stalePush)))
	if recSwitch.Code != http.StatusOK {
		t.Fatalf("switch status = %d", recSwitch.Code)
	}
	var respSwitch browser.SyncResponse
	if err := json.Unmarshal(recSwitch.Body.Bytes(), &respSwitch); err != nil {
		t.Fatal(err)
	}
	if respSwitch.Profile != "work" || respSwitch.Version != 0 {
		t.Fatalf("switch resp: profile=%q ver=%d", respSwitch.Profile, respSwitch.Version)
	}
}

func TestBrowserSyncSizeLimitAndMigration(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	dataDir := t.TempDir()
	r.s.SetLayout(box.Layout{Data: dataDir})

	if _, p := r.s.BrowserCreate(ctx, r.owner, contract.BrowserCreateRequest{Name: "personal"}); p != nil {
		t.Fatal(p)
	}
	name := "personal"
	if _, p := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, Browser: &name}); p != nil {
		t.Fatal(p)
	}

	// Simulate a v1 profile folder + .prev on disk and clear cached jar to emulate engine restart.
	profDir := filepath.Join(dataDir, "browsers", "personal")
	prevDir := filepath.Join(dataDir, "browsers", "personal.prev")
	if err := os.MkdirAll(filepath.Join(profDir, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profDir, "Default", "Cookies"), []byte("sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prevDir, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	r.s.setCachedBrowserJar("personal", nil)

	encVal, err := browser.EncryptV1CookieValue("site.example.com", "migrated-val", 24)
	if err != nil {
		t.Fatal(err)
	}
	r.s.readV1Cookies = func(_ context.Context, _ string) (browser.V1CookieDB, error) {
		return browser.V1CookieDB{
			Version: 24,
			Rows: []browser.V1CookieRow{{
				HostKey: "site.example.com", Name: "session_id", EncryptedValue: encVal, Path: "/",
				IsSecure: true, IsHTTPOnly: true, IsPersistent: false, HasExpires: false, SameSite: -1,
			}},
		}, nil
	}

	// Startup migration converts v1 folder -> jar.json and removes Default/ and .prev/.
	r.s.MigrateBrowsers(ctx)
	if _, err := os.Stat(filepath.Join(profDir, browser.JarFilename)); err != nil {
		t.Fatalf("jar.json missing after MigrateBrowsers: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profDir, "Default")); !os.IsNotExist(err) {
		t.Fatalf("v1 Default folder not deleted: %v", err)
	}
	if _, err := os.Stat(prevDir); !os.IsNotExist(err) {
		t.Fatalf("v1 .prev folder not deleted: %v", err)
	}

	// Box pulling receives the migrated cookie.
	h := r.s.BrowserSync(r.worker, r.room)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/sync", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pull after migration status = %d", rec.Code)
	}
	var resp browser.SyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Version != 1 || len(resp.Upserts) != 1 || resp.Upserts[0].Key.Name != "session_id" {
		t.Fatalf("pull after migration resp: ver=%d upserts=%d", resp.Version, len(resp.Upserts))
	}

	// Oversize push (> 5 MiB) is refused with 413 and leaves jar unchanged.
	bigVal := strings.Repeat("x", 600*1024)
	var big []browser.Cookie
	for i := 0; i < 10; i++ {
		big = append(big, testCookie(t, map[string]any{
			"name": "big" + string(rune('a'+i)), "value": bigVal, "domain": "example.com", "path": "/", "expires": -1, "session": true,
		}))
	}
	bigBody, _ := json.Marshal(browser.SyncRequest{Profile: "personal", BaseVersion: 1, Upserts: big})
	recBig := httptest.NewRecorder()
	h.ServeHTTP(recBig, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(bigBody)))
	if recBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize push status = %d, want 413", recBig.Code)
	}
	if strings.Contains(recBig.Body.String(), bigVal) {
		t.Fatal("oversize error leaked cookie value")
	}
}

func TestBrowserFailedMigrationPreservesV1AndNeverBlocksSync(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	dataDir := t.TempDir()
	r.s.SetLayout(box.Layout{Data: dataDir})

	if _, p := r.s.BrowserCreate(ctx, r.owner, contract.BrowserCreateRequest{Name: "personal"}); p != nil {
		t.Fatal(p)
	}
	name := "personal"
	if _, p := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, Browser: &name}); p != nil {
		t.Fatal(p)
	}

	// Create a v1 Default/Cookies file and force migration to fail.
	profDir := filepath.Join(dataDir, "browsers", "personal")
	cookiesPath := filepath.Join(profDir, "Default", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookiesPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookiesPath, []byte("not-a-valid-sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	r.s.setCachedBrowserJar("personal", nil)
	r.s.readV1Cookies = func(_ context.Context, _ string) (browser.V1CookieDB, error) {
		return browser.V1CookieDB{}, errors.New("corrupt v1 cookies db")
	}

	// Startup migration fails gracefully without deleting v1 files.
	r.s.MigrateBrowsers(ctx)
	if _, err := os.Stat(cookiesPath); err != nil {
		t.Fatalf("v1 Cookies file not preserved after failed migration: %v", err)
	}

	// GET /v1/browser/sync succeeds with an empty jar (never 500).
	h := r.s.BrowserSync(r.worker, r.room)
	recGet := httptest.NewRecorder()
	h.ServeHTTP(recGet, httptest.NewRequest(http.MethodGet, "/v1/browser/sync", nil))
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET sync after failed migration status = %d, want 200", recGet.Code)
	}
	var respGet browser.SyncResponse
	if err := json.Unmarshal(recGet.Body.Bytes(), &respGet); err != nil {
		t.Fatal(err)
	}
	if respGet.Profile != "personal" || respGet.Version != 0 || len(respGet.Upserts) != 0 {
		t.Fatalf("GET sync resp = %+v, want empty personal jar", respGet)
	}

	// POST /v1/browser/sync succeeds in memory without writing jar.json over the preserved v1 folder.
	c := testCookie(t, map[string]any{
		"name": "live", "value": "v1", "domain": "example.com", "path": "/", "expires": -1, "session": true,
	})
	postBody, _ := json.Marshal(browser.SyncRequest{Profile: "personal", BaseVersion: 0, Upserts: []browser.Cookie{c}})
	recPost := httptest.NewRecorder()
	h.ServeHTTP(recPost, httptest.NewRequest(http.MethodPost, "/v1/browser/sync", bytes.NewReader(postBody)))
	if recPost.Code != http.StatusOK {
		t.Fatalf("POST sync after failed migration status = %d, want 200", recPost.Code)
	}
	if _, err := os.Stat(filepath.Join(profDir, browser.JarFilename)); !os.IsNotExist(err) {
		t.Fatalf("jar.json should not be written while v1 migration is pending retry: %v", err)
	}
	if _, err := os.Stat(cookiesPath); err != nil {
		t.Fatalf("v1 Cookies file was lost after sync: %v", err)
	}

	// BrowserList succeeds cleanly as well.
	listRes, p := r.s.BrowserList(ctx, r.owner, contract.BrowserListRequest{})
	if p != nil || len(listRes.Browsers) != 1 {
		t.Fatalf("BrowserList after failed migration: res=%+v err=%+v", listRes, p)
	}
}
