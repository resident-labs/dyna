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

// SuperviseTunnel runs cloudflared as a child and retries failures until shutdown.
// The wake channel forces a check after a link or unlink.
func SuperviseTunnel(ctx context.Context, files *Files, state tunnelState, appPort, cardPort int, wake <-chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var cancel context.CancelFunc
	var done chan error
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
		case <-timer.C:
		case <-done:
			cancel()
			cancel, done = nil, nil
		}
		linked, err := state.ConnectLinked(ctx)
		if err != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		if !linked || appPort == 0 {
			if cancel != nil {
				cancel()
				<-done
				cancel, done = nil, nil
			}
			_ = state.SetConnectConnector(ctx, false, time.Now())
			timer.Reset(5 * time.Second)
			continue
		}
		if cancel != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		path, err := exec.LookPath("cloudflared")
		if err != nil {
			_ = state.SetConnectConnector(ctx, true, time.Now())
			timer.Reset(5 * time.Second)
			continue
		}
		_ = state.SetConnectConnector(ctx, false, time.Now())
		link, err := files.Link()
		if err != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		if _, err := os.Stat(filepath.Join(files.dir, "machine.key")); err != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		key, err := files.Key()
		if err != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		relay := Relay{Base: link.Relay, Key: key}
		if err := relay.ReportOrigin(ctx, link.Machine, appPort, cardPort); err != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		token := filepath.Join(files.dir, "tunnel-token")
		if _, err := os.Stat(token); errors.Is(err, os.ErrNotExist) {
			value, err := relay.TunnelToken(ctx, link.Machine)
			if err != nil || files.SaveTunnelToken(value) != nil {
				timer.Reset(5 * time.Second)
				continue
			}
		} else if err != nil {
			timer.Reset(5 * time.Second)
			continue
		}
		childCtx, stop := context.WithCancel(ctx)
		cmd := exec.CommandContext(childCtx, path, "tunnel", "--no-autoupdate", "run", "--token-file", token)
		cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		if err := cmd.Start(); err != nil {
			stop()
			timer.Reset(5 * time.Second)
			continue
		}
		cancel, done = stop, make(chan error, 1)
		go func(ch chan<- error) { ch <- cmd.Wait() }(done)
		timer.Reset(5 * time.Second)
	}
}
