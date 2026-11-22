package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/browser"
)

func mustCookie(t *testing.T, obj map[string]any) browser.Cookie {
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

// fakeCookieBrowser simulates Chromium's DevTools cookie store, including normalization
// (such as clamping far-future expiry to +400 days).
type fakeCookieBrowser struct {
	mu      sync.Mutex
	cookies map[browser.KeyID]browser.Cookie
	clamp   func(browser.Cookie) browser.Cookie
}

func newFakeCookieBrowser(clamp func(browser.Cookie) browser.Cookie) *fakeCookieBrowser {
	return &fakeCookieBrowser{
		cookies: map[browser.KeyID]browser.Cookie{},
		clamp:   clamp,
	}
}

func (f *fakeCookieBrowser) GetCookies(_ context.Context) ([]browser.Cookie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]browser.Cookie, 0, len(f.cookies))
	for _, c := range f.cookies {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeCookieBrowser) SetCookies(_ context.Context, cookies []browser.Cookie) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range cookies {
		if f.clamp != nil {
			c = f.clamp(c)
		}
		f.cookies[c.Key.ID()] = c
	}
	return nil
}

func (f *fakeCookieBrowser) DeleteCookies(_ context.Context, keys []browser.CookieKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.cookies, k.ID())
	}
	return nil
}

func (f *fakeCookieBrowser) Close() {}

type fakeCookieStore struct {
	mu       sync.Mutex
	profile  string
	pullErr  error
	syncErr  error
	jar      *browser.Jar
	now      func() time.Time
	pulls    int
	syncs    []browser.SyncRequest
}

func newFakeCookieStore(profile string, now func() time.Time) *fakeCookieStore {
	return &fakeCookieStore{
		profile: profile,
		jar:     browser.NewJar(),
		now:     now,
	}
}

func (f *fakeCookieStore) PullCookies(_ context.Context, since int64) (browser.SyncResponse, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls++
	if f.pullErr != nil {
		return browser.SyncResponse{}, false, f.pullErr
	}
	if f.profile == "" {
		return browser.SyncResponse{}, false, nil
	}
	next, resp, _, _, err := f.jar.Sync(f.now(), browser.SyncRequest{BaseVersion: since})
	if err != nil {
		return browser.SyncResponse{}, false, err
	}
	f.jar = next
	resp.Profile = f.profile
	return resp, true, nil
}

func (f *fakeCookieStore) SyncCookies(_ context.Context, req browser.SyncRequest) (browser.SyncResponse, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs = append(f.syncs, req)
	if f.syncErr != nil {
		return browser.SyncResponse{}, false, f.syncErr
	}
	if f.profile == "" {
		return browser.SyncResponse{}, false, nil
	}
	next, resp, _, _, err := f.jar.Sync(f.now(), req)
	if err != nil {
		return browser.SyncResponse{}, false, err
	}
	f.jar = next
	resp.Profile = f.profile
	return resp, true, nil
}

func (f *fakeCookieStore) seed(c browser.Cookie) {
	f.mu.Lock()
	defer f.mu.Unlock()
	next, _, _, _, err := f.jar.Sync(f.now(), browser.SyncRequest{Upserts: []browser.Cookie{c}})
	if err != nil {
		panic(err)
	}
	f.jar = next
}

func TestCookieSyncReadBackPreventsEcho(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	maxExp := float64(now.Add(400 * 24 * time.Hour).Unix())
	farExp := float64(now.Add(1000 * 24 * time.Hour).Unix())

	// Fake Chromium clamps any expiry beyond 400 days.
	clamp := func(c browser.Cookie) browser.Cookie {
		if c.Expires > maxExp {
			var obj map[string]any
			_ = json.Unmarshal(c.Raw, &obj)
			obj["expires"] = maxExp
			b, _ := json.Marshal(obj)
			norm, _ := browser.ParseCookie(b)
			return norm
		}
		return c
	}

	store := newFakeCookieStore("personal", func() time.Time { return now })
	store.seed(mustCookie(t, map[string]any{
		"name": "long_lived", "value": "v1", "domain": "example.com", "path": "/", "expires": farExp, "session": false,
	}))

	chrome := newFakeCookieBrowser(clamp)
	syncer := newCookieSyncer(store, func(context.Context) (cookieSession, error) { return chrome, nil }, func() time.Time { return now })

	// Initial pull applies cookie (clamped by Chromium) and reads back the clamped cookie as baseline.
	if err := syncer.syncOnce(context.Background(), true); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	// Next tick must compute ZERO upserts and ZERO deletes (no echo loop!).
	if err := syncer.syncOnce(context.Background(), true); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(store.syncs) != 1 || len(store.syncs[0].Upserts) != 0 || len(store.syncs[0].Deletes) != 0 {
		t.Fatalf("expected zero echo after initial pull, got upserts=%d deletes=%d", len(store.syncs[0].Upserts), len(store.syncs[0].Deletes))
	}

	// Remote box pushes another far-future cookie; this box pulls it, Chromium clamps it, and next tick has zero echo.
	store.seed(mustCookie(t, map[string]any{
		"name": "remote_far", "value": "v2", "domain": "example.org", "path": "/", "expires": farExp, "session": false,
	}))
	if err := syncer.syncOnce(context.Background(), true); err != nil {
		t.Fatalf("pull remote sync: %v", err)
	}
	if err := syncer.syncOnce(context.Background(), true); err != nil {
		t.Fatalf("post-remote sync: %v", err)
	}
	last := store.syncs[len(store.syncs)-1]
	if len(last.Upserts) != 0 || len(last.Deletes) != 0 {
		t.Fatalf("expected zero echo after remote pull, got upserts=%d deletes=%d", len(last.Upserts), len(last.Deletes))
	}
}

func TestCookieSyncNoPushBeforeFirstPullAndLocalExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := func() time.Time { return now }

	store := newFakeCookieStore("personal", clock)
	expiring := mustCookie(t, map[string]any{
		"name": "short", "value": "s1", "domain": "example.com", "path": "/", "expires": float64(now.Add(5 * time.Minute).Unix()), "session": false,
	})
	live := mustCookie(t, map[string]any{
		"name": "auth", "value": "a1", "domain": "example.com", "path": "/", "expires": float64(now.Add(24 * time.Hour).Unix()), "session": false,
	})
	store.seed(expiring)
	store.seed(live)
	store.pullErr = errors.New("engine temporarily unreachable")

	chrome := newFakeCookieBrowser(nil)
	syncer := newCookieSyncer(store, func(context.Context) (cookieSession, error) { return chrome, nil }, clock)

	// Failed initial pull must not initialize or push anything.
	if err := syncer.syncOnce(context.Background(), true); err == nil {
		t.Fatal("expected initial pull error")
	}
	if len(store.syncs) != 0 {
		t.Fatalf("pushed before first pull succeeded: %d syncs", len(store.syncs))
	}
	// Stop-time sync (allowInit=false) before initial pull must be a no-op and never push deletes.
	if err := syncer.syncOnce(context.Background(), false); err != nil {
		t.Fatalf("stop sync before init: %v", err)
	}
	if len(store.syncs) != 0 {
		t.Fatalf("stop sync pushed before first pull: %d syncs", len(store.syncs))
	}

	// Engine recovers: next tick pulls both cookies and establishes baseline.
	store.pullErr = nil
	if err := syncer.syncOnce(context.Background(), true); err != nil {
		t.Fatalf("recovery pull: %v", err)
	}
	if len(chrome.cookies) != 2 {
		t.Fatalf("chrome cookies after pull = %d, want 2", len(chrome.cookies))
	}

	// Advance clock past expiring's expiry and remove both expiring (natural expiry) and live (logout) from Chromium.
	now = now.Add(10 * time.Minute)
	delete(chrome.cookies, expiring.Key.ID())
	delete(chrome.cookies, live.Key.ID())

	if err := syncer.syncOnce(context.Background(), true); err != nil {
		t.Fatalf("diff sync: %v", err)
	}
	if len(store.syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(store.syncs))
	}
	// Only live should be pushed as a delete; expiring expired locally.
	if len(store.syncs[0].Deletes) != 1 || store.syncs[0].Deletes[0].Name != "auth" {
		t.Fatalf("unexpected deletes pushed: %+v", store.syncs[0].Deletes)
	}
}

func fakeCookieDesktop(t *testing.T, chromiumScript string, store CookieStore, chrome *fakeCookieBrowser, now func() time.Time) (*Process, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "computer"), 0700); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"Xvnc":     "#!/bin/sh\nwhile [ \"$1\" != -rfbunixpath ]; do shift; done\ntouch \"$2\"\nexec sleep 600\n",
		"jwm":      "#!/bin/sh\nexec sleep 600\n",
		"chromium": chromiumScript,
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(filepath.Join(tmp, ".X11-unix"), 0700); err != nil {
		t.Fatal(err)
	}
	p := &Process{
		root:         root,
		socket:       filepath.Join(root, "run", "vnc.sock"),
		tmp:          tmp,
		profile:      filepath.Join(root, "profile"),
		legacyShared: filepath.Join(root, "computer-shared"),
		devtoolsURL:  "http://127.0.0.1:1",
		cookies:      store,
		syncInterval: 50 * time.Millisecond,
		cookieConnect: func(context.Context) (cookieSession, error) {
			return chrome, nil
		},
		now: now,
	}
	if err := os.Mkdir(p.profile, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, root
}

func TestDesktopStartCleansLegacySharedNeverBlocksAndSyncsOnStop(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := func() time.Time { return now }
	store := newFakeCookieStore("personal", clock)
	store.pullErr = errors.New("temporary outage")
	chrome := newFakeCookieBrowser(nil)

	outArgs := filepath.Join(t.TempDir(), "chromium.args")
	script := "#!/bin/sh\necho \"$*\" > " + outArgs + "\nexec sleep 600\n"
	p, _ := fakeCookieDesktop(t, script, store, chrome, clock)

	// Create a leftover v1 /box/computer-shared folder.
	if err := os.MkdirAll(filepath.Join(p.legacyShared, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.legacyShared, "Default", "Cookies"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	// Start must succeed even though initial pull fails, delete legacy /box/computer-shared,
	// and launch Chromium with the per-box profile.
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start blocked by failed pull: %v", err)
	}
	if _, err := os.Stat(p.legacyShared); !os.IsNotExist(err) {
		t.Fatalf("legacy computer-shared not deleted on Start: %v", err)
	}
	waitFor(t, func() bool { data, _ := os.ReadFile(outArgs); return len(data) > 0 })
	args, _ := os.ReadFile(outArgs)
	if !strings.Contains(string(args), "--user-data-dir="+p.profile) {
		t.Fatalf("Chromium did not use per-box profile: %s", args)
	}

	// Recover store; the sync loop retries the initial pull automatically.
	store.mu.Lock()
	store.pullErr = nil
	store.mu.Unlock()
	store.seed(mustCookie(t, map[string]any{
		"name": "initial", "value": "v0", "domain": "example.com", "path": "/", "expires": -1, "session": true,
	}))
	waitFor(t, func() bool {
		chrome.mu.Lock()
		defer chrome.mu.Unlock()
		return len(chrome.cookies) == 1
	})

	// Add a new login cookie right before Stop; Stop must run one final sync before killing Chromium.
	p.syncInterval = time.Hour // ensure the final sync comes from Stop, not the periodic ticker
	loginCookie := mustCookie(t, map[string]any{
		"name": "__Host-newlogin", "value": "fresh", "domain": "app.example.com", "path": "/", "secure": true, "httpOnly": true, "expires": -1, "session": true,
	})
	chrome.mu.Lock()
	chrome.cookies[loginCookie.Key.ID()] = loginCookie
	chrome.mu.Unlock()

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	store.mu.Lock()
	gotCount := store.jar.LiveCount(now)
	store.mu.Unlock()
	if gotCount != 2 {
		t.Fatalf("jar cookie count after Stop = %d, want 2", gotCount)
	}
}

func TestDevToolsCookieRoundTrip(t *testing.T) {
	bin, err := exec.LookPath("chromium")
	if err != nil {
		t.Skip("chromium not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command(bin,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--no-first-run", "--no-default-browser-check", "--password-store=basic",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--user-data-dir="+dir, "about:blank",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	portFile := filepath.Join(dir, "DevToolsActivePort")
	var port int
	waitFor(t, func() bool {
		b, err := os.ReadFile(portFile)
		if err != nil {
			return false
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) < 2 {
			return false
		}
		p, err := strconv.Atoi(strings.TrimSpace(lines[0]))
		if err != nil || p <= 0 {
			return false
		}
		port = p
		return true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dt, err := Connect(ctx, fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer dt.Close()

	future := float64(time.Now().Add(24 * time.Hour).Unix())
	cHost := mustCookie(t, map[string]any{
		"name": "__Host-dt", "value": "h1", "domain": "app.example.com", "path": "/", "secure": true, "httpOnly": true, "sameSite": "Lax", "expires": -1, "session": true,
	})
	cPart := mustCookie(t, map[string]any{
		"name": "part-dt", "value": "p1", "domain": ".sub.example.org", "path": "/p", "secure": true, "sameSite": "None", "expires": future,
		"partitionKey": map[string]any{"topLevelSite": "https://example.net", "hasCrossSiteAncestor": false},
	})
	cKeep := mustCookie(t, map[string]any{
		"name": "sess-dt", "value": "s1", "domain": "example.com", "path": "/", "httpOnly": true, "expires": -1, "session": true,
	})

	if err := dt.SetCookies(ctx, []browser.Cookie{cHost, cPart, cKeep}); err != nil {
		t.Fatalf("SetCookies: %v", err)
	}
	got, err := dt.GetCookies(ctx)
	if err != nil {
		t.Fatalf("GetCookies: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("GetCookies count = %d, want 3", len(got))
	}
	// Passing read-back cookies verbatim into SetCookies must succeed with zero diff on next read-back.
	if err := dt.SetCookies(ctx, got); err != nil {
		t.Fatalf("round-trip SetCookies: %v", err)
	}
	got2, err := dt.GetCookies(ctx)
	if err != nil {
		t.Fatalf("GetCookies 2: %v", err)
	}
	upserts, deletes := browser.DiffCookies(browser.CookieMap(got), got2, time.Now().UTC())
	if len(upserts) != 0 || len(deletes) != 0 {
		t.Fatalf("read-back diff not empty: upserts=%d deletes=%d", len(upserts), len(deletes))
	}

	// Delete __Host- and partitioned cookies via Network.deleteCookies.
	if err := dt.DeleteCookies(ctx, []browser.CookieKey{cHost.Key, cPart.Key}); err != nil {
		t.Fatalf("DeleteCookies: %v", err)
	}
	afterDel, err := dt.GetCookies(ctx)
	if err != nil {
		t.Fatalf("GetCookies after delete: %v", err)
	}
	if len(afterDel) != 1 || afterDel[0].Key.Name != "sess-dt" {
		t.Fatalf("cookies after delete count=%d, want only sess-dt", len(afterDel))
	}
}
