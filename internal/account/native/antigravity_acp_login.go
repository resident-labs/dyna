package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/jsonrpc"
)

// antigravityACPLoginTimeout bounds the wait for the owner's Google sign-in.
const antigravityACPLoginTimeout = 5 * time.Minute

const antigravityACPSettings = `{"auth":{"type":"oauth-personal"}}` + "\n"

var agyAuthURLPattern = regexp.MustCompile(`https://accounts\.google\.com/o/oauth2/v2/auth\?\S+`)

// AntigravityACPLogin runs the ACP server's own Google sign-in in one private
// home. The server uses its own OAuth client, prints the sign-in link on stderr
// and waits for the browser to reach its loopback redirect. It then writes
// acp_token.json under Home.
type AntigravityACPLogin struct {
	Server string // agy_acp_server executable
	// Env is the server's complete environment. Callers overlay
	// runtime.AntigravityACPEnv, the same assembly worker sessions use.
	Env  []string
	Home string // the server's GEMINI_HOME
	Out  io.Writer
	// Expect is the Dyna identity ("google:<sub>") of the account being signed
	// in. A grant for any other Google account is deleted and refused. An empty
	// Expect means the account has no recorded identity yet, so its first
	// sign-in binds the account and capture records the identity afterwards.
	Expect string
	Client claudeHTTPClient // nil uses a bounded default client
}

// AntigravityCLIIdentity names the Google account of the agy CLI token in home,
// or "" when the home has no CLI token. It reads that token file only and never
// calls Google, so it can pin a new ACP sign-in to the login beside it.
func AntigravityCLIIdentity(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", ErrUnsupported
	}
	raw, err := (agyFileStore{path: filepath.Join(home, AntigravityAuthFile)}).Read(context.Background())
	if errors.Is(err, ErrNotSignedIn) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	c, err := agyAccount(raw)
	if err != nil {
		return "", err
	}
	return c.Identity, nil
}

// agyEnvValue returns the last assignment of key in an exec environment.
func agyEnvValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, key+"="); ok {
			value = v
		}
	}
	return value
}

// agyLoopbackAuthURL accepts only the Google authorization endpoint with a
// loopback redirect, so a stray stderr line is never shown as a sign-in link.
func agyLoopbackAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" || u.Path != "/o/oauth2/v2/auth" || len(raw) > 16<<10 {
		return false
	}
	redirect, err := url.Parse(u.Query().Get("redirect_uri"))
	return err == nil && redirect.Scheme == "http" && redirect.Hostname() == "127.0.0.1" && redirect.Port() != ""
}

// Run signs in and returns once the token file is complete. It never prints
// token contents; the owner sees only the sign-in link.
func (l AntigravityACPLogin) Run(ctx context.Context) error {
	if !filepath.IsAbs(l.Home) || l.Server == "" {
		return ErrUnsupported
	}
	// The overlaid AntigravityACPEnv must point the server at this home; a
	// silent mismatch would sign in somewhere Run cannot read back.
	if agyEnvValue(l.Env, "GEMINI_HOME") != l.Home {
		return ErrUnsupported
	}
	dir := filepath.Join(l.Home, filepath.Dir(AntigravityACPTokenFile))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := fsutil.WriteFile(filepath.Join(l.Home, AntigravityACPSettingsFile), []byte(antigravityACPSettings), 0600, true); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, antigravityACPLoginTimeout)
	defer cancel()
	var args []string
	if runtime.GOOS == "linux" {
		args = []string{"--uid="}
	}
	cmd := exec.Command(l.Server, args...)
	cmd.Dir = l.Home
	// TMPDIR holds the roughly 1 GB the server unpacks per launch. The
	// environment keeps it inside the home so the owner can reclaim it with
	// the account; Run only makes sure it exists.
	if tmp := agyEnvValue(l.Env, "TMPDIR"); tmp != "" {
		if err := os.MkdirAll(tmp, 0700); err != nil {
			return err
		}
	}
	cmd.Env = append([]string(nil), l.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	childenv.Clean(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return errors.New("The Antigravity ACP server could not start.")
	}
	killGroup := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var once sync.Once
	conn := jsonrpc.AttachStdio(stdin, stdout, stderr, killGroup, func(line string) {
		if link := agyAuthURLPattern.FindString(line); link != "" && agyLoopbackAuthURL(link) {
			once.Do(func() {
				fmt.Fprintf(l.Out, "Open this link in a browser on this machine and sign in with Google:\n\n%s\n\nWaiting for sign-in (up to 5 minutes).\n", link)
			})
		}
	}, jsonrpc.Handlers{})
	defer func() {
		conn.Close()
		killGroup()
		_ = cmd.Wait()
	}()
	initParams := map[string]any{
		"protocolVersion": 2,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "dyna", "version": "0"},
	}
	if err = conn.Request(ctx, "initialize", initParams, nil); err != nil {
		return errors.New("The Antigravity ACP server did not answer initialize.")
	}
	if err = conn.Notify("initialized", struct{}{}); err != nil {
		return err
	}
	if err = conn.Request(ctx, "authenticate", map[string]any{"methodId": "oauth-personal"}, nil); err != nil {
		if ctx.Err() != nil {
			return errors.New("Google sign-in for Antigravity ACP timed out.")
		}
		return errors.New("Google sign-in for Antigravity ACP did not complete.")
	}
	store := agyFileStore{path: filepath.Join(l.Home, AntigravityACPTokenFile)}
	raw, err := store.Read(ctx)
	if err != nil {
		return errors.New("Antigravity ACP signed in but wrote no token file.")
	}
	if err = agyACPValidate(raw); err != nil {
		return err
	}
	// The grant must belong to the selected account. Never keep one that cannot be proven.
	identity, err := agyACPIdentity(ctx, l.Client, raw, time.Now())
	if err != nil || l.Expect != "" && identity != l.Expect {
		_ = store.remove()
		if err == nil {
			err = errors.New("The Google account you signed in with is not this Antigravity account. The ACP sign-in was discarded.")
		}
		return err
	}
	return nil
}
