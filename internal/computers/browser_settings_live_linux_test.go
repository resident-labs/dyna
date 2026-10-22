//go:build linux

package computers

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/browserconfig"
	"dyna-agent/internal/settings"
)

// This proof uses a private daemon, private desktop sockets and fake owner
// profiles. Headless Chromium exercises the real persistent cookie database.
// It never opens the owner's browser or reads the owner's profile.
//
// Each browser case has its own subtest and the unchanged 10 s budget.
type cookieProof struct {
	ctx          context.Context
	settings     settings.Values
	fixtureURL   string
	launch       func(label, url string) (*Client, *exec.Cmd, string)
	assertCookie func(t *testing.T, ids ...string)
	destroy      func(client *Client) error
}

func newCookieProof(t *testing.T) *cookieProof {
	t.Helper()
	for _, tool := range []string{"labwc", "wlr-randr", "Xwayland"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("live cookie proof needs %s", tool)
		}
	}
	if _, err := os.Stat("/usr/lib/chromium/chromium"); err != nil {
		t.Skip("live cookie proof needs Chromium")
	}
	// Idle shutdown is covered by the desktop tests.
	ctx, _ := longIdleDaemon.use(t)
	// These launchers report races synchronously. Do not add the race runtime's
	// fixed one-second exit sleep to each browser or repeated URL command.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	v := settings.Defaults()
	v.BrowserUseFlagsFile = false
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	fixture, cookies := newCookieServer()
	t.Cleanup(fixture.Close)

	started := map[string]time.Time{}
	destroyed := map[*Client]bool{}
	var destroyMu sync.Mutex
	launch := func(label, targetURL string) (*Client, *exec.Cmd, string) {
		t.Helper()
		id := testSessionID(t, label)
		client := NewClient(id)
		m, err := client.Register(ctx, "browser-settings-proof")
		if err != nil {
			t.Fatal(err)
		}
		env, err := Environment(m, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		target, err := url.Parse(targetURL)
		if err != nil {
			t.Fatal(err)
		}
		query := target.Query()
		query.Set("proof", id)
		target.RawQuery = query.Encode()
		cmd := exec.CommandContext(ctx, envValue(env, "DYNA_CHROMIUM_EXECUTABLE"), "--headless=new", "--no-first-run", "--no-default-browser-check", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--remote-debugging-port=0", target.String())
		cmd.Env = env
		log, err := os.Create(filepath.Join(Dir(id), "cookie-proof.log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = log, log
		started[id] = time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if !destroyed[client] {
				_, _ = client.Stop(context.Background())
			}
			_ = cmd.Wait()
			log.Close()
		})
		return client, cmd, id
	}
	assertCookie := func(t *testing.T, ids ...string) {
		t.Helper()
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		pending := make(map[string]bool, len(ids))
		for _, id := range ids {
			pending[id] = true
		}
		for len(pending) > 0 {
			select {
			case id := <-cookies:
				if pending[id] {
					delete(pending, id)
					t.Logf("Browser %s startup and cookie request took %.3f seconds", id, time.Since(started[id]).Seconds())
				}
			case <-wait.Done():
				for id := range pending {
					b, _ := os.ReadFile(filepath.Join(Dir(id), "cookie-proof.log"))
					t.Errorf("browser %s did not send its cookie: %v; browser log: %s", id, wait.Err(), b)
				}
				t.FailNow()
			}
		}
	}
	destroy := func(client *Client) error {
		started := time.Now()
		defer func() { t.Logf("Browser destroy took %.3f seconds", time.Since(started).Seconds()) }()
		// Destroy stops Chromium and saves its cookies through the same path as
		// Stop. Do it once, then remove this test computer before daemon reset.
		err := Destroy(ctx, client.Session)
		destroyMu.Lock()
		destroyed[client] = err == nil
		destroyMu.Unlock()
		if err == nil {
			err = os.RemoveAll(Dir(client.Session))
		}
		return err
	}
	return &cookieProof{ctx, v, fixture.URL, launch, assertCookie, destroy}
}

func TestBrowserSharedCookieRoundTrip(t *testing.T) {
	p := newCookieProof(t)
	ctx, fixtureURL, launch, assertCookie, destroy := p.ctx, p.fixtureURL, p.launch, p.assertCookie, p.destroy
	first, _, firstID := launch("cookie-shared-one", fixtureURL+"/login")
	if !t.Run("cookie", func(t *testing.T) { assertCookie(t, firstID) }) {
		return
	}
	t.Run("save-and-prepare", func(t *testing.T) {
		repeat := exec.CommandContext(ctx, filepath.Join(Dir(firstID), "bin", "chromium"), fixtureURL+"/repeat")
		if out, err := repeat.CombinedOutput(); err != nil {
			t.Fatalf("repeat URL launch: %v: %s", err, out)
		}
		if len(profileBrowserPIDs(firstID)) == 0 {
			t.Fatal("repeat URL closed Chromium")
		}
		if err := destroy(first); err != nil {
			t.Fatal(err)
		}

		// Prepare the second private profile through the same production entry
		// point as browser.prepare. No browser or desktop uses this profile, so
		// it needs file cleanup rather than a second computer shutdown.
		secondID := testSessionID(t, "cookie-shared-two")
		t.Cleanup(func() { os.RemoveAll(Dir(secondID)) })
		if _, err := prepareChromiumLaunch(ctx, secondID, nil); err != nil {
			t.Fatal(err)
		}
		cookieRow := func(profile string) string {
			t.Helper()
			database := filepath.Join(profile, "Default", "Cookies")
			if _, err := os.Stat(database); os.IsNotExist(err) {
				database = filepath.Join(profile, "Default", "Network", "Cookies")
			}
			query := "SELECT name, hex(value), hex(encrypted_value) FROM cookies WHERE name='dyna_proof';"
			out, err := exec.CommandContext(ctx, "sqlite3", "-readonly", database, query).CombinedOutput()
			if err != nil || !strings.HasPrefix(string(out), "dyna_proof|") {
				t.Fatalf("saved cookie row: %v: %s", err, out)
			}
			return string(out)
		}
		if cookieRow(computerBrowserProfile(secondID)) != cookieRow(browserMaster()) {
			t.Fatal("second computer did not load the master's cookie row")
		}

		t.Log("Shared mode: first computer saved its cookie; a second private profile loaded the same cookie row.")
	})
}

func TestBrowserCopyMineCookieRoundTrip(t *testing.T) {
	p := newCookieProof(t)
	v, fixtureURL, launch, assertCookie, destroy := p.settings, p.fixtureURL, p.launch, p.assertCookie, p.destroy
	owner := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chromium")
	if err := copyBrowserProfile(context.Background(), cookieOwnerSeed, "Default", owner, false); err != nil {
		t.Fatal(err)
	}
	// This is a fake owner profile, never the real Chromium configuration.
	if err := os.Rename(filepath.Join(owner, "Default"), filepath.Join(owner, "Profile 2")); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(owner, "Profile 2", "Cookies"))
	if os.IsNotExist(err) {
		original, err = os.ReadFile(filepath.Join(owner, "Profile 2", "Network", "Cookies"))
	}
	if err != nil {
		t.Fatal(err)
	}
	v.BrowserProfileMode = "copy-mine"
	v.BrowserOwnerProfile = "Profile 2"
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	// Start independent copies together, then probe both within one deadline.
	copied, _, copiedID := launch("cookie-copy-mine", fixtureURL+"/check")
	copiedAgain, _, copiedAgainID := launch("cookie-copy-mine-two", fixtureURL+"/check")
	if !t.Run("cookies", func(t *testing.T) { assertCookie(t, copiedID, copiedAgainID) }) {
		return
	}
	t.Run("shutdown", func(t *testing.T) {
		done := make(chan error, 1)
		go func() { done <- destroy(copied) }()
		secondErr := destroy(copiedAgain)
		firstErr := <-done
		if firstErr != nil || secondErr != nil {
			t.Fatalf("destroy copied browsers: first: %v; second: %v", firstErr, secondErr)
		}
	})
	after, err := os.ReadFile(filepath.Join(owner, "Profile 2", "Cookies"))
	if os.IsNotExist(err) {
		after, err = os.ReadFile(filepath.Join(owner, "Profile 2", "Network", "Cookies"))
	}
	if err != nil || string(after) != string(original) {
		t.Fatal("copy-mine changed the fake owner's cookies")
	}
	t.Log("Copy-mine: two computers loaded the fake owner's cookie and preserved the source database.")
}

// The immutable fake owner is seeded once before measured tests. Each copy-mine
// test still starts two live browsers from independent copies of this profile.
var cookieOwnerSeed string

func prepareCookieOwnerSeed() (func(), error) {
	cleanup := func() {}
	pattern := strings.Split(flag.Lookup("test.run").Value.String(), "/")[0]
	matched, err := regexp.MatchString(pattern, "TestBrowserCopyMineCookieRoundTrip")
	if err != nil || !matched {
		return cleanup, err
	}
	for _, tool := range []string{"labwc", "wlr-randr", "Xwayland", "/usr/lib/chromium/chromium"} {
		if _, err := exec.LookPath(tool); err != nil {
			return cleanup, nil // The live tests report missing tools as skips.
		}
	}
	root, err := os.MkdirTemp("", "cookie-owner-")
	if err != nil {
		return cleanup, err
	}
	cleanup = func() { os.RemoveAll(root) }
	cookieOwnerSeed = filepath.Join(root, "profile")
	fixture, _ := newCookieServer()
	defer fixture.Close()
	// Dump-dom flushes the real cookie database as it exits, and on a loaded
	// machine that exit is slow; the bound only stops a hung browser.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Dump-dom exits after loading the page and flushes the real cookie database.
	// Match production encryption: use the keyring unless this environment
	// requires the basic store. All profile and configuration files stay private.
	args := []string{"--headless=new", "--dump-dom", "--no-first-run", "--no-default-browser-check", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--user-data-dir=" + cookieOwnerSeed}
	if browserconfig.BasicPasswordStore() {
		args = append(args, "--password-store=basic")
	}
	cmd := exec.CommandContext(ctx, "/usr/lib/chromium/chromium", append(args, fixture.URL+"/login")...)
	cmd.Env = mergeEnvironment(os.Environ(), map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"XDG_RUNTIME_DIR": root,
	})
	if out, err := cmd.CombinedOutput(); err != nil {
		return cleanup, fmt.Errorf("seed fake cookie owner: %w: %s", err, out)
	}
	return cleanup, nil
}

// Cookie events come from real browser requests. A unique proof ID prevents
// one computer from satisfying another computer's cookie assertion.
func newCookieServer() (*httptest.Server, <-chan string) {
	cookies := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "dyna_proof", Value: "signed-in", Path: "/", MaxAge: 3600})
			http.Redirect(w, r, "/check?"+r.URL.RawQuery, http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/check" {
			if cookie, err := r.Cookie("dyna_proof"); err == nil && cookie.Value == "signed-in" {
				select {
				case cookies <- r.URL.Query().Get("proof"):
				case <-r.Context().Done():
					return
				}
			}
		}
		fmt.Fprint(w, "<title>Browser login proof</title>Cookie login fixture")
	}))
	return server, cookies
}

func TestCookieFixtureReportsOnlyMatchingCookie(t *testing.T) {
	fixture, cookies := newCookieServer()
	defer fixture.Close()
	client := fixture.Client()
	for _, value := range []string{"", "wrong", "signed-in"} {
		req, err := http.NewRequest(http.MethodGet, fixture.URL+"/check?proof=second-browser", nil)
		if err != nil {
			t.Fatal(err)
		}
		if value != "" {
			req.AddCookie(&http.Cookie{Name: "dyna_proof", Value: value})
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		select {
		case id := <-cookies:
			if value != "signed-in" || id != "second-browser" {
				t.Fatalf("cookie %q produced proof %q", value, id)
			}
		default:
			if value == "signed-in" {
				t.Fatal("valid cookie did not produce its browser proof")
			}
		}
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	response, err := client.Get(fixture.URL + "/login?proof=first-browser")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case id := <-cookies:
		if id != "first-browser" {
			t.Fatalf("login produced proof %q; want first-browser", id)
		}
	default:
		t.Fatal("login did not redirect with its cookie and browser proof")
	}
}
