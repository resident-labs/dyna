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

// fakeAgy stands in for agy: it records its arguments and a few environment values, prints FAKE_LINK on stderr, reads one line, records
// it, writes the token file into the --gemini-dir and exits with FAKE_EXIT. It is never the real program.
const fakeAgy = `#!/bin/sh
for a in "$@"; do args="$args $a"; done
while [ $# -gt 0 ]; do [ "$1" = --gemini-dir ] && dir=$2; shift; done
{ echo "args:$args"; echo "display:$DISPLAY"; echo "proxy:$HTTPS_PROXY"; echo "wayland:$WAYLAND_DISPLAY"; } >> "$FAKE_LOG"
echo "Open this link: $FAKE_LINK" >&2
read line || exit 3
echo "line:$line" >> "$FAKE_LOG"
mkdir -p "$dir/antigravity-acp"
printf %s "` + sentinel + `" > "$dir/antigravity-acp/antigravity-oauth-token"
echo other > "$dir/antigravity-acp/conversation-index"
exit ${FAKE_EXIT:-0}
`

type rig struct {
	t      *testing.T
	layout box.Layout
	logins *Logins
	log    string
	mu     sync.Mutex
	added  map[contract.AccountID]string
	addErr error
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
			r.added[id] = label
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
	if err := r.logins.Paste(started.Attempt, "  4/0AbCdEf  "); err != nil {
		t.Fatal(err)
	}
	r.logins.Wait(started.Attempt)
	status, id, err := r.logins.Status(started.Attempt)
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
	log := r.logged()
	for _, want := range []string{"args: acp --login --gemini-dir ", "--app-data-dir antigravity-acp", "display:\n", "proxy:\n", "wayland:\n", "line:4/0AbCdEf\n"} {
		if !strings.Contains(log, want) {
			t.Fatalf("agy saw %q, missing %q", log, want)
		}
	}
}

func TestPageAddressSendsItsCode(t *testing.T) {
	r := newRig(t, validLinkText)
	started, _ := r.logins.Start("a")
	if err := r.logins.Paste(started.Attempt, "https://antigravity.google/oauth-callback?state=ST1&code=4%2F0Xyz"); err != nil {
		t.Fatal(err)
	}
	r.logins.Wait(started.Attempt)
	if !strings.Contains(r.logged(), "line:4/0Xyz\n") {
		t.Fatalf("agy got %q", r.logged())
	}
}

func TestBadPasteIsRefusedAndNothingReachesAgy(t *testing.T) {
	r := newRig(t, validLinkText)
	started, _ := r.logins.Start("a")
	for _, bad := range []string{"", "   ", "two words", "a\x00b", strings.Repeat("x", maxCode+1), "http://evil.example/?code=1&state=OTHER",
		"https://antigravity.google/oauth-callback?state=OTHER&code=1", "https://antigravity.google/oauth-callback?state=ST1", "https://antigravity.google/oauth-callback?state=ST1&code=a%20b"} {
		if err := r.logins.Paste(started.Attempt, bad); !errors.Is(err, ErrPaste) {
			t.Fatalf("%.30q: %v", bad, err)
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
	for name, link := range map[string]string{
		"host":     "https://accounts.google.com.evil.example/o/oauth2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"path":     "https://accounts.google.com/o/oauth2/v2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"redirect": "https://accounts.google.com/o/oauth2/auth?response_type=code&state=a&redirect_uri=" + url.QueryEscape("https://evil.example/cb"),
		"state":    "https://accounts.google.com/o/oauth2/auth?response_type=code&state=a&state=b&redirect_uri=" + url.QueryEscape(redirectURI),
		"nostate":  "https://accounts.google.com/o/oauth2/auth?response_type=code&redirect_uri=" + url.QueryEscape(redirectURI),
		"type":     "https://accounts.google.com/o/oauth2/auth?response_type=token&state=a&redirect_uri=" + url.QueryEscape(redirectURI),
		"none":     "no link here",
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

func TestWrongExitStoresNothing(t *testing.T) {
	r := newRig(t, validLinkText)
	t.Setenv("FAKE_EXIT", "1")
	started, _ := r.logins.Start("a")
	if err := r.logins.Paste(started.Attempt, "code"); err != nil {
		t.Fatal(err)
	}
	r.logins.Wait(started.Attempt)
	if status, _, _ := r.logins.Status(started.Attempt); status != contract.ConnectFailed || len(r.added) != 0 || len(r.left()) != 0 || len(r.folders()) != 0 {
		t.Fatalf("status %q added %v left %v %v", status, r.added, r.left(), r.folders())
	}
}

func TestRowFailureRemovesTheTokenCopy(t *testing.T) {
	r := newRig(t, validLinkText)
	r.addErr = errors.New("store down")
	started, _ := r.logins.Start("a")
	r.logins.Paste(started.Attempt, "code")
	r.logins.Wait(started.Attempt)
	if status, _, _ := r.logins.Status(started.Attempt); status != contract.ConnectFailed || len(r.left()) != 0 {
		t.Fatalf("status %q left %v", status, r.left())
	}
}

func TestTimeoutKillsAgyAndStoresNothing(t *testing.T) {
	r := newRig(t, validLinkText)
	r.logins.cfg.Life = 50 * time.Millisecond
	started, _ := r.logins.Start("a")
	r.logins.Wait(started.Attempt)
	if status, _, _ := r.logins.Status(started.Attempt); status != contract.ConnectExpired || len(r.left()) != 0 || len(r.folders()) != 0 {
		t.Fatalf("status %q left %v %v", status, r.left(), r.folders())
	}
	if err := r.logins.Paste(started.Attempt, "late"); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("paste after expiry: %v", err)
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
	if _, _, err := r.logins.Status(first.Attempt); !errors.Is(err, ErrNoAttempt) {
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

func TestNewDeletesFoldersACrashLeft(t *testing.T) {
	r := newRig(t, validLinkText)
	stale := r.layout.HostAgyLogin("stale")
	os.MkdirAll(filepath.Join(stale, "antigravity-acp"), 0o700)
	New(Config{Layout: r.layout})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale folder: %v", err)
	}
}
