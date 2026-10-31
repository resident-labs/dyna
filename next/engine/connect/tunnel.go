package connect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type tunnelState interface {
	ConnectLinked(context.Context) (bool, error)
	SetConnectConnector(context.Context, bool, time.Time) error
}

type tunnelHooks struct {
	find    func() (string, error)
	prepare func(context.Context) (string, error)
	start   func(context.Context, string, string) (context.CancelFunc, <-chan error, error)
	now     func() time.Time
	ticks   <-chan time.Time
	retry   func()
}

// SuperviseTunnel runs cloudflared as a child and retries failures until shutdown.
func SuperviseTunnel(ctx context.Context, files *Files, state tunnelState, appPort, cardPort int, wake <-chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	h := tunnelHooks{
		find: func() (string, error) { return exec.LookPath("cloudflared") },
		now:  time.Now, ticks: timer.C, retry: func() { timer.Reset(5 * time.Second) },
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
		start: func(ctx context.Context, path, token string) (context.CancelFunc, <-chan error, error) {
			childCtx, stop := context.WithCancel(ctx)
			cmd := exec.CommandContext(childCtx, path, "tunnel", "--no-autoupdate", "run", "--token-file", token)
			cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			if err := cmd.Start(); err != nil {
				stop()
				return nil, nil, err
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			return stop, done, nil
		},
	}
	superviseTunnel(ctx, state, appPort != 0, wake, h)
}

func superviseTunnel(ctx context.Context, state tunnelState, enabled bool, wake <-chan struct{}, h tunnelHooks) {
	var cancel context.CancelFunc
	var done <-chan error
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-h.ticks:
		case <-done:
			cancel()
			cancel, done = nil, nil
		}
		linked, err := state.ConnectLinked(ctx)
		if err != nil {
			h.retry()
			continue
		}
		if !linked || !enabled {
			if cancel != nil {
				cancel()
				<-done
				cancel, done = nil, nil
			}
			_ = state.SetConnectConnector(ctx, false, h.now())
			h.retry()
			continue
		}
		if cancel != nil {
			h.retry()
			continue
		}
		path, err := h.find()
		if err != nil {
			_ = state.SetConnectConnector(ctx, true, h.now())
			h.retry()
			continue
		}
		_ = state.SetConnectConnector(ctx, false, h.now())
		token, err := h.prepare(ctx)
		if err != nil {
			h.retry()
			continue
		}
		cancel, done, err = h.start(ctx, path, token)
		if err != nil {
			cancel, done = nil, nil
		}
		h.retry()
	}
}
