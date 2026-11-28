package agylogin

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const sentinel = "SENTINEL-TOKEN-9f3a"

var validLinkText = "https://accounts.google.com/o/oauth2/auth?client_id=c&response_type=code&state=ST1&redirect_uri=" + url.QueryEscape(redirectURI)

// fakeAgy stands in for agy: it records its arguments and a few environment values, prints FAKE_LINK on stdout as agy does (indented, as the
// last line of a short text), then FAKE_ERR on stderr if set, reads one line, records it, writes the token file into the --gemini-dir
// and exits with FAKE_EXIT. FAKE_FAST_EXIT makes it exit at once with no link; FAKE_LINK_STDERR prints the link on stderr instead. It
// is never the real program.
const fakeAgy = `#!/bin/sh
[ -n "$FAKE_FAST_EXIT" ] && { echo "startup failed" >&2; exit 1; }
for a in "$@"; do args="$args $a"; done
while [ $# -gt 0 ]; do [ "$1" = --gemini-dir ] && dir=$2; shift; done
{ echo "args:$args"; echo "display:$DISPLAY"; echo "proxy:$HTTPS_PROXY"; echo "wayland:$WAYLAND_DISPLAY"; } >> "$FAKE_LOG"
if [ -n "$FAKE_LINK_STDERR" ]; then echo "  $FAKE_LINK" >&2; else printf 'Open this link in a browser to log in with Google:\n\n  %s\n\n' "$FAKE_LINK"; fi
[ -n "$FAKE_ERR" ] && echo "$FAKE_ERR" >&2
read line || exit 3
echo "line:$line" >> "$FAKE_LOG"
[ "${FAKE_EXIT:-0}" != 0 ] && exit $FAKE_EXIT
mkdir -p "$dir/antigravity-acp"
printf %s "` + sentinel + `" > "$dir/antigravity-acp/antigravity-oauth-token"
echo other > "$dir/antigravity-acp/conversation-index"
echo "Logged in as person@example.com"
exit 0
`

type rig struct {
	t      *testing.T
	layout box.Layout
	logins *Logins
	log    string
	mu     sync.Mutex
	added  map[contract.AccountID]string
	addErr error
	// atAdd is what the token folder held when the row was added.
	atAdd []string
}

func newRig(t *testing.T, link string) *rig {
	t.Helper()
	data := t.TempDir()
	r := &rig{t: t, layout: box.Layout{Data: data, HostData: data}, log: filepath.Join(data, "fake.log"), added: map[contract.AccountID]string{}}
	if err := os.MkdirAll(filepath.Dir(r.layout.HostAgyBinary()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.layout.HostAgyBinary(), []byte(fakeAgy), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LOG", r.log)
	t.Setenv("FAKE_LINK", link)
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:1")
	n := 0
	r.logins = New(Config{Layout: r.layout, Life: time.Minute, FindLink: 5 * time.Second,
		NewID: func() (contract.AccountID, error) {
			n++
			return contract.AccountID("agy_test" + string(rune('0'+n))), nil
		},
		Add: func(_ context.Context, id contract.AccountID, label string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.addErr != nil {
				return r.addErr
			}
			entries, _ := os.ReadDir(r.layout.HostAgyTokens())
			r.atAdd = nil
			for _, e := range entries {
				r.atAdd = append(r.atAdd, e.Name())
			}
			r.added[id] = label
			return nil
		},
		Remove: func(_ context.Context, id contract.AccountID) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			delete(r.added, id)
			return nil
		}})
	return r
}

func (r *rig) logged() string {
	b, _ := os.ReadFile(r.log)
	return string(b)
}

// left lists every file under the host data folder except the fake program and its log.
func (r *rig) left() []string {
	var names []string
	filepath.WalkDir(r.layout.HostData, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path != r.layout.HostAgyBinary() && path != r.log {
			rel, _ := filepath.Rel(r.layout.HostData, path)
			names = append(names, rel)
		}
		return nil
	})
	return names
}

func (r *rig) folders() []string {
	m, _ := filepath.Glob(r.layout.HostAgyLoginPattern())
	return m
}

func TestSignInStoresOnlyTheTokenAndDeletesTheFolder(t *testing.T) {
	r := newRig(t, validLinkText)
	started, err := r.logins.Start("second")
	if err != nil || started.Link != validLinkText {
		t.Fatalf("start: %+v %v", started, err)
	}
	// 4/0A... is the shape of the code Google's page shows.
	if err := r.logins.Paste(started.Attempt, "  4/0AbCdEf-_.~  "); err != nil {
		t.Fatal(err)
	}
	r.logins.Wait(started.Attempt)
	status, _, id, err := r.logins.Status(started.Attempt)
	if err != nil || status != contract.ConnectConnected || id == "" || r.added[id] != "second" {
		t.Fatalf("status %q %q %v added %v", status, id, err, r.added)
	}
	token, err := os.ReadFile(r.layout.HostAgyToken(string(id)))
	if err != nil || string(token) != sentinel {
		t.Fatalf("token %q %v", token, err)
	}
	if info, _ := os.Stat(r.layout.HostAgyToken(string(id))); info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v", info.Mode())
	}
	if left := r.left(); len(left) != 1 || left[0] != filepath.Join("agy", "tokens", string(id)) {
		t.Fatalf("left behind: %v", left)
	}
	if len(r.folders()) != 0 {
		t.Fatalf("login folder left: %v", r.folders())
	}
	// The row went in while the token was still a half copy, so a crash between the two leaves a listed account and no token.
	if len(r.atAdd) != 1 || !strings.HasPrefix(r.atAdd[0], ".copy-") {
		t.Fatalf("token folder at the row's insert: %v", r.atAdd)
	}
	log := r.logged()
	for _, want := range []string{"args: acp --login --gemini-dir ", "--app-data-dir antigravity-acp", "display:\n", "proxy:\n", "wayland:\n", "line:4/0AbCdEf-_.~\n"} {
		if !strings.Contains(log, want) {
			t.Fatalf("agy saw %q, missing %q", log, want)
		}
	}
}

// What agy is sent for a page address is the whole address, as T3 sends it, never the code decoded out of it.
func TestPageAddressIsSentWhole(t *testing.T) {
	r := newRig(t, validLinkText)
	started, _ := r.logins.Start("a")
	page := "https://antigravity.google/oauth-callback?state=ST1&code=4%2F0Xyz"
	if err := r.logins.Paste(started.Attempt, " "+page+"\n"); err != nil {
		t.Fatal(err)
	}
	r.logins.Wait(started.Attempt)
	if !strings.Contains(r.logged(), "line:"+page+"\n") {
		t.Fatalf("agy got %q", r.logged())
	}
}

func TestPasteChecksAreT3s(t *testing.T) {
	r := newRig(t, validLinkText)
	started, _ := r.logins.Start("a")
	page := func(query string) string { return "https://antigravity.google/oauth-callback?" + query }
	for name, bad := range map[string]string{
		"empty":         "",
		"blank":         "   ",
		"two words":     "two words",
		"control":       "a\x00b",
		"too long code": strings.Repeat("x", maxCode+1),
		"odd character": "4/0A=bc",
		"other origin":  "https://evil.example/oauth-callback?state=ST1&code=1",
		"other path":    "https://antigravity.google/other?state=ST1&code=1",
		"http scheme":   "http://antigravity.google/oauth-callback?state=ST1&code=1",
		"userinfo":      "https://user:pw@antigravity.google/oauth-callback?state=ST1&code=1",
		"wrong state":   page("state=OTHER&code=1"),
		"no state":      page("code=1"),
		"two states":    page("state=ST1&state=ST1&code=1"),
		"no code":       page("state=ST1"),
		"empty code":    page("state=ST1&code="),
		"two codes":     page("state=ST1&code=1&code=2"),
		"space in it":   page("state=ST1&code=a%20b") + " x",
		"too long page": page("state=ST1&code=" + strings.Repeat("x", maxURL)),
	} {
		if err := r.logins.Paste(started.Attempt, bad); !errors.Is(err, ErrPaste) {
			t.Fatalf("%s %.40q: %v", name, bad, err)
		}
	}
	if err := r.logins.Paste("nope", "code"); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("unknown attempt: %v", err)
	}
	if strings.Contains(r.logged(), "line:") {
		t.Fatalf("agy was written to: %q", r.logged())
	}
	// The refused texts did not use up the sign-in: a good one still works, and only once.
	if err := r.logins.Paste(started.Attempt, "good"); err != nil {
		t.Fatal(err)
	}
	if err := r.logins.Paste(started.Attempt, "again"); !errors.Is(err, ErrPaste) && !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("second paste: %v", err)
	}
	r.logins.Wait(started.Attempt)
}

func TestBadLinkIsRefusedAndLeavesNothing(t *testing.T) {
	q := func(extra string) string {
		return "https://accounts.google.com/o/oauth2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape(redirectURI) + extra
	}
	for name, link := range map[string]string{
		"host":          "https://accounts.google.com.evil.example/o/oauth2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"path":          "https://accounts.google.com/o/oauth2/v2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"redirect":      "https://accounts.google.com/o/oauth2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape("https://evil.example/cb"),
		"two states":    q("&state=b"),
		"no state":      "https://accounts.google.com/o/oauth2/auth?response_type=code&redirect_uri=" + url.QueryEscape(redirectURI),
		"empty state":   "https://accounts.google.com/o/oauth2/auth?response_type=code&state=&redirect_uri=" + url.QueryEscape(redirectURI),
		"long state":    "https://accounts.google.com/o/oauth2/auth?response_type=code&state=" + strings.Repeat("s", maxState+1) + "&redirect_uri=" + url.QueryEscape(redirectURI),
		"two redirects": q("&redirect_uri=" + url.QueryEscape(redirectURI)),
		"two types":     q("&response_type=code"),
		"token type":    "https://accounts.google.com/o/oauth2/auth?response_type=token&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"userinfo":      "https://u:p@accounts.google.com/o/oauth2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"fragment":      q("#frag"),
		"whitespace":    q("&x=a b"),
		"too long":      q("&x=" + strings.Repeat("x", maxURL)),
		"none":          "no link here",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, link)
			r.logins.cfg.FindLink = 2 * time.Second
			if _, err := r.logins.Start("a"); !errors.Is(err, ErrLink) {
				t.Fatalf("start: %v", err)
			}
			if len(r.folders()) != 0 || len(r.left()) != 0 {
				t.Fatalf("left: %v %v", r.folders(), r.left())
			}
			if _, err := r.logins.Start("b"); !errors.Is(err, ErrLink) {
				t.Fatalf("a refused link left the runner busy: %v", err)
			}
		})
	}
}

// T3 takes the link from a stdout line that starts with Google's address, and from nowhere else.
func TestOnlyAStdoutLineThatStartsWithTheLinkCounts(t *testing.T) {
	r := newRig(t, validLinkText)
	r.logins.cfg.FindLink = time.Second
	t.Setenv("FAKE_LINK_STDERR", "1")
	if _, err := r.logins.Start("a"); !errors.Is(err, ErrLink) {
		t.Fatalf("a link on stderr: %v", err)
	}
	t.Setenv("FAKE_LINK_STDERR", "")
	t.Setenv("FAKE_LINK", "Open: "+validLinkText)
	if _, err := r.logins.Start("a"); !errors.Is(err, ErrLink) {
		t.Fatalf("a link in the middle of a line: %v", err)
	}
}

func TestAStdoutLineOverTheBoundIsRefused(t *testing.T) {
	if readLines(strings.NewReader(strings.Repeat("a", maxLine+1)), func(string) {}) {
		t.Fatal("a line past the bound was accepted")
	}
	var lines []string
	if !readLines(strings.NewReader("one\ntwo\r\nthree"), func(s string) { lines = append(lines, s) }) || len(lines) != 3 || lines[1] != "two" {
		t.Fatalf("lines %q", lines)
	}
}

func TestWrongExitStoresNothing(t *testing.T) {
	r := newRig(t, validLinkText)
	t.Setenv("FAKE_EXIT", "1")
	started, _ := r.logins.Start("a")
	if err := r.logins.Paste(started.Attempt, "code"); err != nil {
		t.Fatal(err)
	}
	r.logins.Wait(started.Attempt)
	if status, reason, _, _ := r.logins.Status(started.Attempt); status != contract.ConnectFailed || reason != "" || len(r.added) != 0 || len(r.left()) != 0 || len(r.folders()) != 0 {
		t.Fatalf("status %q %q added %v left %v %v", status, reason, r.added, r.left(), r.folders())
	}
}

// A refusal in agy's output, a 403 or a verification or restriction, is said as such, whichever stream it came on.
func TestARefusalInTheOutputIsClassed(t *testing.T) {
	for _, text := range []string{"Error 403: access denied", "Your account needs verification", "This account is restricted"} {
		r := newRig(t, validLinkText)
		t.Setenv("FAKE_EXIT", "1")
		t.Setenv("FAKE_ERR", text)
		started, _ := r.logins.Start("a")
		r.logins.Paste(started.Attempt, "code")
		r.logins.Wait(started.Attempt)
		status, reason, _, _ := r.logins.Status(started.Attempt)
		if status != contract.ConnectFailed || reason != contract.ConnectRestricted || len(r.added) != 0 || len(r.left()) != 0 {
			t.Fatalf("%q: status %q reason %q added %v left %v", text, status, reason, r.added, r.left())
		}
	}
}

func TestRowFailureStoresNothing(t *testing.T) {
	r := newRig(t, validLinkText)
	r.addErr = errors.New("store down")
	started, _ := r.logins.Start("a")
	r.logins.Paste(started.Attempt, "code")
	r.logins.Wait(started.Attempt)
	if status, _, _, _ := r.logins.Status(started.Attempt); status != contract.ConnectFailed || len(r.left()) != 0 {
		t.Fatalf("status %q left %v", status, r.left())
	}
}

func TestTimeoutKillsAgyAndStoresNothing(t *testing.T) {
	r := newRig(t, validLinkText)
	r.logins.cfg.Life = 50 * time.Millisecond
	started, _ := r.logins.Start("a")
	r.logins.Wait(started.Attempt)
	if status, _, _, _ := r.logins.Status(started.Attempt); status != contract.ConnectExpired || len(r.left()) != 0 || len(r.folders()) != 0 {
		t.Fatalf("status %q left %v %v", status, r.left(), r.folders())
	}
	if err := r.logins.Paste(started.Attempt, "late"); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("paste after expiry: %v", err)
	}
}

// Cancel is on the engine: it kills the waiting agy, deletes the folder, and a new sign-in starts at once.
func TestCancelThenAddWorksAtOnce(t *testing.T) {
	r := newRig(t, validLinkText)
	first, err := r.logins.Start("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.logins.Cancel(first.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(r.folders()) != 0 || len(r.left()) != 0 {
		t.Fatalf("left after cancel: %v %v", r.folders(), r.left())
	}
	second, err := r.logins.Start("b")
	if err != nil {
		t.Fatalf("add after cancel: %v", err)
	}
	if err := r.logins.Paste(first.Attempt, "late"); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("the cancelled attempt took a paste: %v", err)
	}
	if err := r.logins.Cancel("nope"); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("cancel of an unknown attempt: %v", err)
	}
	r.logins.Paste(second.Attempt, "code")
	r.logins.Wait(second.Attempt)
	if status, _, _, _ := r.logins.Status(second.Attempt); status != contract.ConnectConnected {
		t.Fatalf("second: %q", status)
	}
}

func TestSecondSignInWhileOneRunsIsRefused(t *testing.T) {
	r := newRig(t, validLinkText)
	first, err := r.logins.Start("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.logins.Start("b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second: %v", err)
	}
	r.logins.Paste(first.Attempt, "code")
	r.logins.Wait(first.Attempt)
	second, err := r.logins.Start("b")
	if err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
	if _, _, _, err := r.logins.Status(first.Attempt); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("only the last attempt is kept: %v", err)
	}
	r.logins.Paste(second.Attempt, "code")
	r.logins.Wait(second.Attempt)
}

func TestNoProgram(t *testing.T) {
	r := newRig(t, validLinkText)
	os.Remove(r.layout.HostAgyBinary())
	if _, err := r.logins.Start("a"); !errors.Is(err, ErrNoProgram) {
		t.Fatalf("%v", err)
	}
}

func TestNewDeletesWhatACrashLeft(t *testing.T) {
	r := newRig(t, validLinkText)
	stale := r.layout.HostAgyLogin("stale")
	os.MkdirAll(filepath.Join(stale, "antigravity-acp"), 0o700)
	os.MkdirAll(r.layout.HostAgyTokens(), 0o700)
	half := filepath.Join(r.layout.HostAgyTokens(), ".copy-123")
	kept := r.layout.HostAgyToken("agy_kept")
	os.WriteFile(half, []byte("x"), 0o600)
	os.WriteFile(kept, []byte("x"), 0o600)
	New(Config{Layout: r.layout})
	for _, gone := range []string{stale, half} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s left: %v", gone, err)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("a stored token was deleted: %v", err)
	}
}

// An agy that exits before it prints a link fails the start at once, and leaves the runner free and no folder behind.
func TestExitBeforeALinkFailsPromptlyAndFreesTheRunner(t *testing.T) {
	r := newRig(t, validLinkText)
	t.Setenv("FAKE_FAST_EXIT", "1")
	result := make(chan error, 1)
	go func() { _, err := r.logins.Start("a"); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, ErrLink) {
			t.Fatalf("start: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("start did not return after agy exited")
	}
	if len(r.folders()) != 0 || len(r.left()) != 0 {
		t.Fatalf("left: %v %v", r.folders(), r.left())
	}
	t.Setenv("FAKE_FAST_EXIT", "")
	second, err := r.logins.Start("b")
	if err != nil {
		t.Fatalf("the runner stayed held: %v", err)
	}
	r.logins.Paste(second.Attempt, "code")
	r.logins.Wait(second.Attempt)
}
