package model

import (
	"context"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Supervise restarts the proxy until the engine stops.
func (p *Proxy) Supervise(ctx context.Context, executable string) {
	file, err := p.Listener.File()
	if err != nil {
		log.Printf("model proxy listener: %v", err)
		return
	}
	defer file.Close()
	supervise(ctx, time.Now, time.After, func() <-chan error {
		done := make(chan error, 1)
		cmd := exec.CommandContext(ctx, executable, "proxy", "--config", p.Config)
		cmd.ExtraFiles = []*os.File{file}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = 2 * time.Second
		stdin, err := cmd.StdinPipe()
		if err != nil {
			done <- err
			return done
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			done <- err
			return done
		}
		go func() {
			done <- cmd.Wait()
			_ = stdin.Close()
		}()
		return done
	})
}

func supervise(ctx context.Context, now func() time.Time, after func(time.Duration) <-chan time.Time, start func() <-chan error) {
	delay := time.Second
	for ctx.Err() == nil {
		began := now()
		err := <-start()
		if ctx.Err() != nil {
			return
		}
		log.Printf("model proxy exited: %v", err)
		if now().Sub(began) >= time.Minute {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-after(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}
