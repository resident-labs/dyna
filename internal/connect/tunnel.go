package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/notices"
)

// tokenWatchInterval is how often the connector checks its credential. A new
// link or an unlink takes effect without an engine restart, so the child must
// stop soon rather than at the next engine restart.
var tokenWatchInterval = 2 * time.Second

// runConnector runs one cloudflared attempt. A test replaces it with a fake child.
var runConnector = runOnce

// lookConnector resolves the connector binary. A test replaces it with an
// immediate answer, so no test searches a real PATH.
var lookConnector = exec.LookPath

// connectorPause is the clock the loop waits on between attempts. A test
// replaces it with a channel it sends itself, so no test waits for a clock.
var connectorPause = time.After

// postTunnelAlert reports one missing connector as a typed owner notice, and
// clearTunnelAlert removes it when the connector runs again. RunTunnel calls
// each once per transition. Tests replace them.
var postTunnelAlert = postMissingConnectorNotice
var clearTunnelAlert = clearMissingConnectorNotice

// tokenTicks is the source of credential checks. A test replaces it with ticks
// it sends itself, so no test waits for a clock.
var tokenTicks = func() (<-chan time.Time, func()) {
	ticker := time.NewTicker(tokenWatchInterval)
	return ticker.C, ticker.Stop
}

// RunTunnel runs cloudflared for the managed tunnel until ctx ends, and
// starts it again with backoff when it exits. The token goes in by file
// (--token-file), never on the command line, so ps does not show it. A changed
// or removed token stops the child, so the next attempt uses the new link or
// waits for one. A connector binary that is not on PATH posts one typed owner
// notice and keeps waiting, instead of retrying silently forever. cloudflared's
// own output goes to <connect dir>/cloudflared.log.
func RunTunnel(ctx context.Context, st *Store, bin string, logf func(string, ...any)) {
	if bin == "" {
		bin = "cloudflared"
	}
	wait := 5 * time.Second
	// delivered says this process posted the missing-connector alert, and
	// cleared says it proved no stale alert remains. Both start unset, so a
	// restart re-clears an alert an earlier process left behind, and only a
	// successful post or clear moves the state: a failed one retries on the
	// next attempt instead of suppressing the alert forever.
	delivered, cleared := false, false
	for ctx.Err() == nil {
		token, err := os.ReadFile(st.TunnelTokenPath())
		if err != nil {
			logf("connect: no tunnel token yet; the tunnel waits for the relay")
		} else if resolved, lookErr := lookConnector(bin); lookErr != nil {
			if !delivered {
				logf("connect: %s is not on PATH; the tunnel waits for it to be installed", bin)
				if err := postTunnelAlert(bin, os.Getenv("PATH")); err != nil {
					logf("connect: report the missing connector: %v", err)
				} else {
					delivered = true
				}
			}
		} else {
			if delivered || !cleared {
				if err := clearTunnelAlert(); err != nil {
					logf("connect: clear the missing-connector alert: %v", err)
				} else {
					cleared, delivered = true, false
				}
			}
			start := time.Now()
			runCtx, stop := context.WithCancel(ctx)
			changed := make(chan struct{}, 1)
			ticks, stopTicks := tokenTicks()
			go watchTunnelToken(runCtx, st.TunnelTokenPath(), token, ticks, stop, changed, logf)
			err := runConnector(runCtx, st, resolved)
			stopTicks()
			stop()
			if ctx.Err() != nil {
				return
			}
			// A new credential starts the connector at once; a crash backs off.
			select {
			case <-changed:
				continue
			default:
			}
			if time.Since(start) > 2*time.Minute {
				wait = 5 * time.Second
			}
			logf("connect: cloudflared stopped (%v); starting it again in %s", err, wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-connectorPause(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

// credentialDiffers reports whether the credential the connector started with is
// gone or different. It is a pure decision, so a test can check it without a
// clock or a child process.
func credentialDiffers(now []byte, err error, seen []byte) bool {
	return err != nil || !bytes.Equal(now, seen)
}

// watchTunnelToken stops the connector when its credential changes or goes away.
// A link writes a new token, and an unlink removes it; the child in flight still
// holds the old tunnel, so it must not keep running.
func watchTunnelToken(ctx context.Context, path string, seen []byte, ticks <-chan time.Time, stop context.CancelFunc, changed chan<- struct{}, logf func(string, ...any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		now, err := os.ReadFile(path)
		if !credentialDiffers(now, err, seen) {
			continue
		}
		if err != nil {
			logf("connect: the tunnel token is gone; stopping cloudflared")
		} else {
			logf("connect: the tunnel token changed; restarting cloudflared")
		}
		select {
		case changed <- struct{}{}:
		default:
		}
		stop()
		return
	}
}

// missingConnectorKey is the owner notice for a connector that is not on
// PATH. One key, so a still-missing binary replaces its own alert instead of
// stacking one per retry.
const missingConnectorKey = "tunnel:connector-missing"

// postMissingConnectorNotice reports the missing connector with the binary it
// tried and the PATH it searched, so the owner sees where to install it.
func postMissingConnectorNotice(binary, path string) error {
	_, err := notices.DefaultStore().Post(notices.Notice{
		Key:      missingConnectorKey,
		Kind:     notices.KindTunnel,
		Severity: notices.SeverityError,
		Tunnel:   &notices.TunnelFacts{Binary: binary, Path: path},
	})
	return err
}

// clearMissingConnectorNotice removes the missing-connector alert when the
// connector runs again.
func clearMissingConnectorNotice() error {
	return notices.DefaultStore().DismissKey(missingConnectorKey)
}

func runOnce(ctx context.Context, st *Store, bin string) error {
	logf, err := os.OpenFile(filepath.Join(st.Dir(), "cloudflared.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "run", "--token-file", st.TunnelTokenPath())
	cmd.Stdout, cmd.Stderr = logf, logf
	// Keep only what cloudflared needs; never pass a Dyna stamp along.
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	// If serve dies without a clean shutdown, take cloudflared with it, so
	// a restart never leaves an old connector running.
	dieWithParent(cmd)
	cmd.WaitDelay = 10 * time.Second
	return childenv.Clean(cmd).Run()
}

// TailscaleRoute finds a tailscale serve setup that points at both
// loopback ports, and returns it as a route. ok is false when there is
// none: Tailscale is not running, or serve is not set up for Dyna.
func TailscaleRoute(ctx context.Context, appPort, cardPort int) (Route, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := childenv.Clean(exec.CommandContext(ctx, "tailscale", "serve", "status", "--json")).Output()
	if err != nil {
		return Route{}, false
	}
	return parseTailscaleServe(out, appPort, cardPort)
}

func parseTailscaleServe(out []byte, appPort, cardPort int) (Route, bool) {
	var st struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
	}
	if json.Unmarshal(out, &st) != nil {
		return Route{}, false
	}
	find := func(port int) (host, hostport string) {
		for hp, w := range st.Web {
			p := w.Handlers["/"].Proxy
			for _, want := range []string{"http://127.0.0.1:" + strconv.Itoa(port), "http://localhost:" + strconv.Itoa(port), "127.0.0.1:" + strconv.Itoa(port)} {
				if strings.TrimSuffix(p, "/") == want {
					h, _, _ := strings.Cut(hp, ":")
					return h, hp
				}
			}
		}
		return "", ""
	}
	ah, ahp := find(appPort)
	ch, chp := find(cardPort)
	if ah == "" || ch == "" || ah != ch {
		return Route{}, false
	}
	origin := func(hp string) string { return "https://" + strings.TrimSuffix(hp, ":443") }
	r := Route{Kind: "tailscale"}
	r.App, r.Cards = origin(ahp), origin(chp)
	return r, true
}

// ErrUnlinked means the relay no longer knows this machine.
var ErrUnlinked = errors.New("the relay says this machine was unlinked; run dyna engine connect unlink --local, then link again")

// Sync is the start-up check with the relay: refresh the key set, read the
// link's state, report the loopback ports and routes, and fetch the tunnel
// token when it is missing. It returns an error the caller logs; nothing
// here blocks the server.
func (s *Service) Sync(ctx context.Context, appPort, cardPort int) error {
	l, ok := s.link()
	if !ok {
		return nil
	}
	rl := s.relay(l)
	if ks, err := rl.FetchKeys(ctx); err == nil {
		_ = s.o.Store.SaveKeySet(ks)
		s.mu.Lock()
		s.keys = ks
		s.mu.Unlock()
	}
	info, err := rl.Status(ctx, l.Machine)
	var re *RelayError
	if errors.As(err, &re) && re.Status == 410 {
		l.Unlinked = true
		_ = s.o.Store.SaveLink(l)
		s.endSessions(func(Session) bool { return true }, "the relay says this machine was unlinked")
		return ErrUnlinked
	}
	if err != nil {
		return fmt.Errorf("relay status: %w", err)
	}
	if info.Epoch > l.Epoch {
		l = s.bumpEpoch(l, info.Epoch)
	}
	if info.Owner.Name != l.Owner.Name || info.Name != l.Name {
		l.Owner.Name, l.Name = info.Owner.Name, info.Name
		_ = s.o.Store.SaveLink(l)
	}
	if err := rl.ReportOrigin(ctx, l.Machine, appPort, cardPort); err != nil {
		return fmt.Errorf("report ports: %w", err)
	}
	if err := rl.ReportRoutes(ctx, l.Machine, s.RelayRoutes()); err != nil {
		return fmt.Errorf("report routes: %w", err)
	}
	if _, err := os.Stat(s.o.Store.TunnelTokenPath()); err != nil {
		tok, err := rl.TunnelToken(ctx, l.Machine)
		if err != nil {
			return fmt.Errorf("tunnel token: %w", err)
		}
		if err := s.o.Store.SaveTunnelToken(tok); err != nil {
			return err
		}
	}
	return nil
}
