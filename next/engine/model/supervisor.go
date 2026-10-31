package model

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/engine/child"
)

// Supervise restarts the proxy until the engine stops.
func (p *Proxy) Supervise(ctx context.Context, executable string, st AuthStore, complete func(context.Context, OAuthCompletion) error, connections *Connections) {
	file, err := p.Listener.File()
	if err != nil {
		log.Printf("model proxy listener: %v", err)
		return
	}
	defer file.Close()
	child.Run(ctx, nil, time.Now, time.After, func(childCtx context.Context) <-chan error {
		done := make(chan error, 1)
		p.generation.Add(1)
		if err := st.PruneUnboundProxyAuths(ctx); err != nil {
			done <- err
			return done
		}
		cmd := exec.CommandContext(childCtx, executable, "proxy", "--config", p.Config)
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			done <- err
			return done
		}
		parent := os.NewFile(uintptr(fds[0]), "proxy-store-parent")
		child := os.NewFile(uintptr(fds[1]), "proxy-store-child")
		cmd.ExtraFiles = []*os.File{file, child}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = 2 * time.Second
		stdin, err := cmd.StdinPipe()
		if err != nil {
			_ = parent.Close()
			_ = child.Close()
			done <- err
			return done
		}
		// Upstream logs can include provider identities and token-exchange responses.
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			_ = parent.Close()
			_ = child.Close()
			_ = stdin.Close()
			done <- err
			return done
		}
		_ = child.Close()
		go serveAuthRPC(parent, st, complete, connections)
		go func() {
			done <- cmd.Wait()
			_ = parent.Close()
			_ = stdin.Close()
		}()
		return done
	}, func(err error) { log.Printf("model proxy exited: %v", err) })
}
