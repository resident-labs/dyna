// Package agylogin adds an Antigravity account: it runs agy's own sign-in (`agy acp --login`) in a scratch profile folder, hands the
// owner the Google link it prints, takes the code the owner pastes back, and keeps ONLY the resulting token file under the account's
// opaque id. Dyna never reads inside a profile folder: the token is copied whole, by name, and is never parsed, logged or stored
// anywhere but its file. The link and paste checks, the stdin line and the process handling follow T3 Code's codeLogin
// (AntigravityDriver.ts:418-490) and antigravityAuthSupport.ts:576-645; the differences are listed in the PR body.
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
)

var (
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
	maxURL      = 16_384 // T3 maxAuthorizationUrlLength
	maxCode     = 2048
	maxState    = 512
	maxLine     = 16 << 20   // T3 maxStdoutLineBytes
	maxKeep     = 2 * maxURL // how much of a line is kept: a link past maxURL still reads as too long after its indent is trimmed
	linkStart   = "https://accounts.google.com/"
	defaultLife = 5 * time.Minute
	defaultFind = 30 * time.Second
)

// Config is what a Logins needs. Publish makes an account exist: it records the row, runs install (which puts the token file in place)
// and undoes the row if install fails, all under the lock Remove takes, so a Remove never sees half of it. NewID makes the opaque id the
// file is named by.
type Config struct {
	Layout  box.Layout
	Publish func(ctx context.Context, id contract.AccountID, label string, install func() error) error
	NewID   func() (contract.AccountID, error)
	Now     func() time.Time
	// Life is how long a sign-in may take (5 minutes) and FindLink how long agy has to print its link (30 seconds); tests shorten them.
	Life, FindLink time.Duration
}

// Logins runs one sign-in at a time and remembers the last one's result. A new Start ends the one still waiting.
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
	cancel  chan struct{} // closed by Cancel
	done    chan struct{} // closed once the sign-in has ended and its folder is gone
	exited  chan struct{} // closed once agy has exited; exitErr is set before the close, so any number of receives see it
	exitErr error

	mu      sync.Mutex
	pasted  bool
	status  contract.ConnectStatus
	account contract.AccountID
}

// New returns the sign-in runner, after deleting the sign-in folders and half-written token copies an engine crash left behind.
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
	for _, pattern := range []string{cfg.Layout.HostAgyLoginPattern(), cfg.Layout.HostAgyCopyPattern()} {
		left, _ := filepath.Glob(pattern)
		for _, path := range left {
			os.RemoveAll(path)
		}
	}
	return &Logins{cfg: cfg}
}

// Started is a sign-in that printed its link.
type Started struct {
	Attempt string
	Link    string
	Expires time.Time
}

// Start runs agy's sign-in and returns the link it prints. The sign-in then waits for Paste, for at most Life. A sign-in still waiting
// is ended first (killed, joined, its scratch folder deleted), so a lost Cancel or a quick Cancel then Add is never refused. Only the
// owner starts one, so replacing is safe; a Paste for the replaced attempt finds no such sign-in.
func (l *Logins) Start(label string) (Started, error) {
	if _, err := os.Stat(l.cfg.Layout.HostAgyBinary()); err != nil {
		return Started{}, ErrNoProgram
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil {
		l.cur.stop()
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Started{}, err
	}
	a := &attempt{id: hex.EncodeToString(raw[:]), label: label, status: contract.ConnectPending, cancel: make(chan struct{}), done: make(chan struct{}), exited: make(chan struct{}), expires: l.cfg.Now().Add(l.cfg.Life)}
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
	// agy is told no proxy and no display: the engine host is the exit, and without a display it prints the link and opens no browser.
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
	stdout, outW, err := os.Pipe()
	if err != nil {
		os.RemoveAll(a.dir)
		return "", err
	}
	stderr, errW, err := os.Pipe()
	if err != nil {
		stdout.Close()
		outW.Close()
		os.RemoveAll(a.dir)
		return "", err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{stdout, outW, stderr, errW} {
			f.Close()
		}
		os.RemoveAll(a.dir)
		return "", err
	}
	outW.Close()
	errW.Close()
	a.cmd, a.stdin = cmd, stdin
	// The link is a trimmed stdout line that starts with Google's address, as in T3. Every other line, and all of stderr, is read so agy
	// never blocks on a full pipe, and dropped: nothing agy prints is parsed, kept or logged.
	found := make(chan string, 1)
	go func() {
		defer stdout.Close()
		sent := false
		ok := readLines(stdout, func(line string) {
			if line = strings.TrimFunc(line, isSpace); !sent && strings.HasPrefix(line, linkStart) {
				sent = true
				found <- line
			}
		})
		if !ok && !sent {
			found <- ""
		}
	}()
	go func() {
		defer stderr.Close()
		readLines(stderr, func(string) {})
	}()
	go func() { a.exitErr = cmd.Wait(); close(a.exited) }()
	find := time.NewTimer(l.cfg.FindLink)
	defer find.Stop()
	select {
	case link := <-found:
		if state, ok := validLink(link); ok {
			a.state = state
			go l.finish(a)
			return link, nil
		}
	case <-a.exited:
	case <-find.C:
	}
	killGroup(cmd)
	<-a.exited
	os.RemoveAll(a.dir)
	return "", ErrLink
}

// readLines calls each with every line of r, without its line end, and reports false when one passes maxLine, after which it drains r
// unread. A line over maxKeep is passed as its first maxKeep bytes.
func readLines(r io.Reader, each func(string)) bool {
	reader := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	size := 0
	for {
		part, isPrefix, err := reader.ReadLine()
		size += len(part)
		if size > maxLine {
			io.Copy(io.Discard, reader)
			return false
		}
		if len(line) < maxKeep {
			line = append(line, part[:min(len(part), maxKeep-len(line))]...)
		}
		if err != nil {
			return true
		}
		if !isPrefix {
			each(string(line))
			line, size = line[:0], 0
		}
	}
}

// validLink is T3's check of the authorization URL for the code flow (antigravityAuthSupport.ts:579-612): Google's page, no userinfo or
// fragment, no whitespace, one each of state, redirect_uri and response_type, and that redirect exactly. It returns the state.
func validLink(raw string) (state string, ok bool) {
	if len(raw) > maxURL || strings.ContainsFunc(raw, isSpace) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" || u.EscapedPath() != "/o/oauth2/auth" || u.User != nil || u.Fragment != "" {
		return "", false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["redirect_uri"]) != 1 || len(q["response_type"]) != 1 {
		return "", false
	}
	state = q.Get("state")
	if q.Get("response_type") != "code" || state == "" || len(state) > maxState || strings.ContainsFunc(state, isSpace) || q.Get("redirect_uri") != redirectURI {
		return "", false
	}
	return state, true
}

// validResponse is T3's antigravityLoginResponse (antigravityAuthSupport.ts:620-645): a code of [\w./~-]{1,2048}, or the page's address,
// whose origin and path are the redirect, with no userinfo, one state equal to this sign-in's and one non-empty code. What is returned is
// exactly what agy is sent: the code, or the whole address, undecoded.
func validResponse(state, response string) (string, bool) {
	value := strings.TrimFunc(response, isSpace)
	if len(value) >= 1 && len(value) <= maxCode && !strings.ContainsFunc(value, func(r rune) bool {
		return !(r < 128 && (r == '_' || r == '.' || r == '/' || r == '~' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'))
	}) {
		return value, true
	}
	if len(value) > maxURL || strings.ContainsFunc(value, isSpace) {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme+"://"+u.Host+u.EscapedPath() != redirectURI || u.User != nil {
		return "", false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["state"]) != 1 || q.Get("state") != state || len(q["code"]) != 1 || q.Get("code") == "" {
		return "", false
	}
	return value, true
}

// isSpace is JavaScript's \s, which T3's whitespace checks use.
func isSpace(r rune) bool {
	return r == ' ' || r >= '\t' && r <= '\r' || r == 0xA0 || r == 0x1680 || r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
}

func killGroup(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// finish ends the sign-in and deletes its folder. It first joins with the process: agy exits by itself, or a Cancel or the time limit
// kills it and the exit is waited for. Then the exit decides, not who asked: a clean exit (agy took the code and wrote its token) stores
// the account even when a Cancel or the time limit came in the same moment, because the sign-in did happen; a Cancel after the exit
// changes nothing. Any other exit is a failure, or an expiry when the time limit ended it. Nothing agy printed is read for the verdict.
func (l *Logins) finish(a *attempt) {
	defer close(a.done)
	defer os.RemoveAll(a.dir)
	timeout := time.NewTimer(l.cfg.Life)
	defer timeout.Stop()
	expired := false
	select {
	case <-a.exited:
	case <-a.cancel:
		killGroup(a.cmd)
		<-a.exited
	case <-timeout.C:
		expired = true
		killGroup(a.cmd)
		<-a.exited
	}
	if a.exitErr != nil {
		if expired {
			a.set(contract.ConnectExpired, "")
		} else {
			a.set(contract.ConnectFailed, "")
		}
		return
	}
	id, err := l.store(a)
	if err != nil {
		a.set(contract.ConnectFailed, "")
		return
	}
	a.set(contract.ConnectConnected, id)
}

// store copies the token file whole beside its final name, then publishes: the account row first and the file's rename after, in one
// critical section with Remove. A crash between the two leaves a listed account with no sign-in, which the owner sees and can remove;
// the half copy is deleted at the next start.
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
	err = l.cfg.Publish(context.Background(), id, a.label, func() error {
		return os.Rename(tmp.Name(), l.cfg.Layout.HostAgyToken(string(id)))
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Paste sends the owner's code to the waiting sign-in, if it is a code or the page's address that answers this sign-in (validResponse).
// Anything else is refused and nothing reaches agy.
func (l *Logins) Paste(attemptID, text string) error {
	a := l.attempt(attemptID)
	if a == nil || a.get() != contract.ConnectPending {
		return ErrNoAttempt
	}
	line, ok := validResponse(a.state, text)
	if !ok {
		return ErrPaste
	}
	a.mu.Lock()
	first := !a.pasted
	a.pasted = true
	a.mu.Unlock()
	if !first {
		return ErrPaste
	}
	// The write is outside the lock: agy may never read its input, and Cancel must still get through. Cancel kills agy, which ends a
	// blocked write with an error.
	if _, err := io.WriteString(a.stdin, line+"\n"); err != nil {
		return fmt.Errorf("%w: the sign-in has ended", ErrNoAttempt)
	}
	return nil
}

// stop ends the sign-in and returns once it has ended and its folder is gone. Stopping one that has ended is a no-op.
func (a *attempt) stop() {
	a.mu.Lock()
	select {
	case <-a.cancel:
	default:
		close(a.cancel)
	}
	a.mu.Unlock()
	<-a.done
}

// Cancel ends the sign-in that waits: it kills agy, waits for it, and deletes the folder, so a new Start works at once. A sign-in that
// has already ended is left as it is.
func (l *Logins) Cancel(attemptID string) error {
	a := l.attempt(attemptID)
	if a == nil {
		return ErrNoAttempt
	}
	a.stop()
	return nil
}

// Status is the result of the last sign-in: pending while it waits, then connected with the new account, failed or expired.
func (l *Logins) Status(attemptID string) (contract.ConnectStatus, contract.AccountID, error) {
	a := l.attempt(attemptID)
	if a == nil {
		return "", "", ErrNoAttempt
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status, a.account, nil
}

// Owns says whether the attempt is the last sign-in this runner started, finished or not.
func (l *Logins) Owns(attemptID string) bool { return l.attempt(attemptID) != nil }

// Wait returns once the attempt has ended and its folder is deleted. An unknown attempt returns at once.
func (l *Logins) Wait(attemptID string) {
	if a := l.attempt(attemptID); a != nil {
		<-a.done
	}
}

func (l *Logins) attempt(id string) *attempt {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur == nil || l.cur.id != id {
		return nil
	}
	return l.cur
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
