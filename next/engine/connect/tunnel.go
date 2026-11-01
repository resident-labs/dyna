package connect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/child"
)

type tunnelState interface {
	ConnectLinked(context.Context) (bool, error)
	ConnectAttempt(context.Context) (contract.ConnectAttempt, error)
	SetConnectConnector(context.Context, bool, time.Time) error
}

type tunnelHooks struct {
	find    func() (string, error)
	prepare func(context.Context) (string, error)
	start   func(context.Context, string, string) <-chan error
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
}

// SuperviseTunnel runs cloudflared as a child and retries failures until shutdown.
func SuperviseTunnel(ctx context.Context, files *Files, state tunnelState, appPort, cardPort int, routes func(context.Context) []string, wake <-chan struct{}) {
	h := tunnelHooks{
		find: func() (string, error) { return exec.LookPath("cloudflared") }, now: time.Now, after: time.After,
		prepare: func(ctx context.Context) (string, error) {
			link, err := files.Link()
			if err != nil {
				return "", err
			}
			if _, err := os.Stat(filepath.Join(files.dir, "machine.key")); err != nil {
				return "", err
			}
			key, err := files.Key()
			if err != nil {
				return "", err
			}
			relay := Relay{Base: link.Relay, Key: key}
			if err := relay.ReportOrigin(ctx, link.Machine, appPort, cardPort); err != nil {
				return "", err
			}
			if err := relay.reportRoutes(ctx, link, routes(ctx)); err != nil {
				return "", err
			}
			token := filepath.Join(files.dir, "tunnel-token")
			if _, err := os.Stat(token); errors.Is(err, os.ErrNotExist) {
				value, err := relay.TunnelToken(ctx, link.Machine)
				if err != nil {
					return "", err
				}
				if err := files.SaveTunnelToken(value); err != nil {
					return "", err
				}
			} else if err != nil {
				return "", err
			}
			return token, nil
		},
		start: func(ctx context.Context, path, token string) <-chan error {
			done := make(chan error, 1)
			cmd := exec.CommandContext(ctx, path, "tunnel", "--no-autoupdate", "run", "--token-file", token)
			cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			if err := cmd.Start(); err != nil {
				done <- err
				return done
			}
			go func() { done <- cmd.Wait() }()
			return done
		},
	}
	superviseTunnel(ctx, state, appPort != 0, wake, h)
}

func superviseTunnel(ctx context.Context, state tunnelState, enabled bool, wake <-chan struct{}, h tunnelHooks) {
	child.Run(ctx, wake, h.now, h.after, func(ctx context.Context) <-chan error {
		fail := func(err error) <-chan error { done := make(chan error, 1); done <- err; return done }
		linked, err := state.ConnectLinked(ctx)
		if err != nil {
			return fail(err)
		}
		a, err := state.ConnectAttempt(ctx)
		if err != nil {
			return fail(err)
		}
		if !linked || !enabled || a.State == contract.ConnectAttemptUnlinking {
			_ = state.SetConnectConnector(ctx, false, h.now())
			return nil
		}
		path, err := h.find()
		if err != nil {
			_ = state.SetConnectConnector(ctx, true, h.now())
			return fail(err)
		}
		_ = state.SetConnectConnector(ctx, false, h.now())
		token, err := h.prepare(ctx)
		if err != nil {
			return fail(err)
		}
		return h.start(ctx, path, token)
	}, func(error) {})
}

// TailscaleRoute discovers existing Serve mappings; it never changes them.
func TailscaleRoute(ctx context.Context, appPort int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tailscale", "serve", "status", "--json")
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseTailscaleServe(out, appPort)
}

func parseTailscaleServe(out []byte, appPort int) (string, error) {
	var status struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return "", err
	}
	port := strconv.Itoa(appPort)
	for hostPort, web := range status.Web {
		switch strings.TrimSuffix(web.Handlers["/"].Proxy, "/") {
		case "http://127.0.0.1:" + port, "http://localhost:" + port, "127.0.0.1:" + port:
			return "https://" + strings.TrimSuffix(hostPort, ":443"), nil
		}
	}
	return "", nil
}
