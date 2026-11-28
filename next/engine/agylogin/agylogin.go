// Package agylogin adds an Antigravity account: it runs agy's own sign-in (`agy acp --login`) in a scratch profile folder, hands the
// owner the Google link it prints, takes the code the owner pastes back, and keeps ONLY the resulting token file under the account's
// opaque id. Dyna never reads inside a profile folder: the token is copied whole, by name, and is never parsed, logged or stored
// anywhere but its file.
package agylogin

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
)

var (
	// ErrBusy means a sign-in is already running: one at a time.
	ErrBusy = errors.New("an Antigravity sign-in is already running")
	// ErrNoProgram means the agy program is not installed on this host.
	ErrNoProgram = errors.New("the Antigravity program is not installed on this host")
	// ErrLink means agy printed no sign-in link, or one that is not Google's authorization page for Antigravity.
	ErrLink = errors.New("Antigravity printed no valid sign-in link")
	// ErrPaste means the pasted text is not a code or a page address that belongs to this sign-in. Nothing was sent to agy.
	ErrPaste = errors.New("that is not the code for this sign-in")
	// ErrNoAttempt means the attempt is not the sign-in that is waiting.
	ErrNoAttempt = errors.New("no such sign-in is waiting")
)

const (
	redirectURI = "https://antigravity.google/oauth-callback"
	// tokenName is the one file kept, inside the profile folder's --app-data-dir.
	tokenName   = "antigravity-acp/antigravity-oauth-token"
	maxToken    = 1 << 20
	maxCode     = 2048
	defaultLife = 5 * time.Minute
	defaultFind = 30 * time.Second
)

var linkPattern = regexp.MustCompile(`https://accounts\.google\.com/[^\s"'<>]+`)

// Config is what a Logins needs. Add records the account row once its token file is stored; NewID makes the opaque id the file is named by.
type Config struct {
	Layout box.Layout
	Add    func(ctx context.Context, id contract.AccountID, label string) error
	NewID  func() (contract.AccountID, error)
	Now    func() time.Time
	// Life is how long a sign-in may take (5 minutes) and FindLink how long agy has to print its link (30 seconds); tests shorten them.
	Life, FindLink time.Duration
}

// Logins runs one sign-in at a time and remembers the last one's result.
type Logins struct {
	cfg Config
	mu  sync.Mutex
	cur *attempt
}

type attempt struct {
	id      string
	label   string
	state   string // the link's state, which a pasted page address must carry
	dir     string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	expires time.Time

	done    chan struct{} // closed once the sign-in has ended and its folder is gone
	mu      sync.Mutex
	pasted  bool
	status  contract.ConnectStatus
	account contract.AccountID
}

// New returns the sign-in runner, after deleting the sign-in folders an engine crash left behind.
func New(cfg Config) *Logins {
	if cfg.Life == 0 {
		cfg.Life = defaultLife
	}
	if cfg.FindLink == 0 {
		cfg.FindLink = defaultFind
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	left, _ := filepath.Glob(cfg.Layout.HostAgyLoginPattern())
	for _, dir := range left {
		os.RemoveAll(dir)
	}
	return &Logins{cfg: cfg}
}

// Started is a sign-in that printed its link.
type Started struct {
	Attempt string
	Link    string
	Expires time.Time
}

// Start runs agy's sign-in and returns the link it prints. The sign-in then waits for Paste, for at most Life.
func (l *Logins) Start(label string) (Started, error) {
	if _, err := os.Stat(l.cfg.Layout.HostAgyBinary()); err != nil {
		return Started{}, ErrNoProgram
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil && l.cur.get() == contract.ConnectPending {
		return Started{}, ErrBusy
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Started{}, err
	}
	a := &attempt{id: hex.EncodeToString(raw[:]), label: label, status: contract.ConnectPending, done: make(chan struct{}), expires: l.cfg.Now().Add(l.cfg.Life)}
	link, err := l.launch(a)
	if err != nil {
		return Started{}, err
	}
	l.cur = a
	return Started{Attempt: a.id, Link: link, Expires: a.expires}, nil
}

// launch runs agy for a, waits for its link and validates it, and leaves a goroutine that ends the sign-in. On an error nothing is left.
func (l *Logins) launch(a *attempt) (string, error) {
	a.dir = l.cfg.Layout.HostAgyLogin(a.id)
	tmp := filepath.Join(a.dir, "tmp")
	cwd := filepath.Join(a.dir, "cwd")
	for _, dir := range []string{tmp, cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			os.RemoveAll(a.dir)
			return "", err
		}
	}
	// agy is told no proxy and no display: the engine host is the exit, and a sign-in has no browser to open.
	var base []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "DISPLAY", "WAYLAND_DISPLAY", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		base = append(base, kv)
	}
	cmd := exec.Command(l.cfg.Layout.HostAgyBinary(), agy.AcpArgs(a.dir, "--login")...)
	cmd.Dir = cwd
	cmd.Env = agy.Environment(base, a.dir, tmp, "")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(a.dir)
		return "", err
	}
	out, in, err := os.Pipe()
	if err != nil {
		os.RemoveAll(a.dir)
		return "", err
	}
	// One pipe for both streams, so a link on either is found. Nothing read from it is logged.
	cmd.Stdout, cmd.Stderr = in, in
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		os.RemoveAll(a.dir)
		return "", err
	}
	in.Close()
	a.cmd, a.stdin = cmd, stdin
	found := make(chan string, 1)
	go func() {
		defer out.Close()
		reader := bufio.NewReaderSize(out, 64<<10)
		sent := false
		for {
			line, err := reader.ReadString('\n')
			if m := linkPattern.FindString(line); m != "" && !sent {
				sent = true
				found <- m
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	find := time.NewTimer(l.cfg.FindLink)
	defer find.Stop()
	var link string
	select {
	case link = <-found:
		if state, ok := validLink(link); ok {
			a.state = state
			go l.finish(a, exited)
			return link, nil
		}
	case <-exited:
	case <-find.C:
	}
	killGroup(cmd)
	<-exited
	os.RemoveAll(a.dir)
	return "", ErrLink
}

// validLink accepts only Google's authorization page for the Antigravity redirect, with one state, and returns that state.
func validLink(raw string) (state string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" || u.Path != "/o/oauth2/auth" {
		return "", false
	}
	q := u.Query()
	if len(q["state"]) != 1 || q.Get("state") == "" || q.Get("response_type") != "code" || q.Get("redirect_uri") != redirectURI {
		return "", false
	}
	return q.Get("state"), true
}

func killGroup(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// finish ends the sign-in: on a clean exit it stores the token and the account, and whatever the end it deletes the folder.
func (l *Logins) finish(a *attempt, exited <-chan error) {
	defer close(a.done)
	defer os.RemoveAll(a.dir)
	timeout := time.NewTimer(l.cfg.Life)
	defer timeout.Stop()
	select {
	case err := <-exited:
		if err != nil {
			a.set(contract.ConnectFailed, "")
			return
		}
		id, err := l.store(a)
		if err != nil {
			a.set(contract.ConnectFailed, "")
			return
		}
		a.set(contract.ConnectConnected, id)
	case <-timeout.C:
		killGroup(a.cmd)
		<-exited
		a.set(contract.ConnectExpired, "")
	}
}

// store copies the token file whole into the token folder under a fresh id and records the account; if the row fails, the copy goes.
func (l *Logins) store(a *attempt) (contract.AccountID, error) {
	id, err := l.cfg.NewID()
	if err != nil {
		return "", err
	}
	src, err := os.OpenFile(filepath.Join(a.dir, filepath.FromSlash(tokenName)), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if info, err := src.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("the sign-in left no token file")
	}
	folder := l.cfg.Layout.HostAgyTokens()
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(folder, ".copy-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(src, maxToken+1))
	if err == nil && (n == 0 || n > maxToken) {
		err = errors.New("the token file is empty or too large")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	dest := l.cfg.Layout.HostAgyToken(string(id))
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	if err := l.cfg.Add(context.Background(), id, a.label); err != nil {
		os.Remove(dest)
		return "", err
	}
	return id, nil
}

// Paste sends the owner's code to the waiting sign-in. It accepts the bare code or the address of the page Google sent the owner to, whose
// state must be this sign-in's and which carries the code. A text that is neither is refused and nothing reaches agy.
func (l *Logins) Paste(attemptID, text string) error {
	l.mu.Lock()
	a := l.cur
	l.mu.Unlock()
	if a == nil || a.id != attemptID || a.get() != contract.ConnectPending {
		return ErrNoAttempt
	}
	code := strings.TrimSpace(text)
	if strings.HasPrefix(code, "http") {
		u, err := url.Parse(code)
		if err != nil || u.Query().Get("state") != a.state {
			return ErrPaste
		}
		code = u.Query().Get("code")
	}
	if !validCode(code) {
		return ErrPaste
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pasted {
		return ErrPaste
	}
	a.pasted = true
	if _, err := io.WriteString(a.stdin, code+"\n"); err != nil {
		return fmt.Errorf("%w: the sign-in has ended", ErrNoAttempt)
	}
	return nil
}

func validCode(code string) bool {
	return code != "" && len(code) <= maxCode && !strings.HasPrefix(code, "http") && !strings.ContainsFunc(code, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// Status is the result of the last sign-in: pending while it waits, then connected with the new account, failed or expired.
func (l *Logins) Status(attemptID string) (contract.ConnectStatus, contract.AccountID, error) {
	l.mu.Lock()
	a := l.cur
	l.mu.Unlock()
	if a == nil || a.id != attemptID {
		return "", "", ErrNoAttempt
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status, a.account, nil
}

func (a *attempt) get() contract.ConnectStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *attempt) set(status contract.ConnectStatus, account contract.AccountID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.account = status, account
}

// Owns says whether the attempt is the last sign-in this runner started, finished or not.
func (l *Logins) Owns(attemptID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cur != nil && l.cur.id == attemptID
}

// Wait returns once the attempt has ended and its folder is deleted. An unknown attempt returns at once.
func (l *Logins) Wait(attemptID string) {
	l.mu.Lock()
	a := l.cur
	l.mu.Unlock()
	if a != nil && a.id == attemptID {
		<-a.done
	}
}
