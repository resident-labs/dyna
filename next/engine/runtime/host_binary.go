package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

// installBinary shares one progress-aware operation per host, with subscriber-owned cancellation.
func (b *Boxes) installBinary(ctx context.Context, h *podman.Host) error {
	if _, err := b.binarySum(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.installs == nil {
		b.installs = make(map[*podman.Host]*binaryInstall)
	}
	job := b.installs[h]
	if job != nil && job.closing {
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-job.done:
		}
		b.mu.Lock()
		if b.installs[h] == job {
			delete(b.installs, h)
		}
		b.mu.Unlock()
		return b.installBinary(ctx, h)
	}
	if job == nil {
		parent := b.Context
		if parent == nil {
			parent = context.WithoutCancel(ctx)
		}
		work, cancel := context.WithCancel(parent)
		job = &binaryInstall{done: make(chan struct{}), cancel: cancel, progress: make(chan struct{}, 1)}
		b.installs[h] = job
		go func() {
			idle, stop := podman.IdleContext(work, box.StartStepBudget, job.progress)
			defer stop()
			progress := podman.WithByteProgress(idle, func(n int) {
				if n > 0 {
					select {
					case job.progress <- struct{}{}:
					default:
					}
				}
			})
			job.err = h.InstallBinary(progress, b.Layout.HostBin(), b.Version, b.binSum, b.compressedBinary)
			if job.err != nil && errors.Is(context.Cause(idle), podman.ErrTransferIdle) {
				job.err = errors.Join(podman.ErrTransferIdle, job.err)
			}
			close(job.done)
		}()
	}
	job.refs++
	b.mu.Unlock()
	var err error
	select {
	case <-job.done:
		err = job.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	b.mu.Lock()
	job.refs--
	last := job.refs == 0
	if last {
		job.closing = true
		job.cancel()
	}
	b.mu.Unlock()
	if last {
		<-job.done
		// Joining can confirm a successful frame after the subscriber's signal.
		// Only the last waiter has joined; other canceled subscribers must leave promptly.
		if job.err == nil {
			err = nil
		}
		b.mu.Lock()
		if b.installs[h] == job {
			delete(b.installs, h)
		}
		b.mu.Unlock()
	}
	return err
}

func (b *Boxes) compressedBinary(encoding podman.BinaryEncoding) ([]byte, error) {
	b.binCacheOnce.Do(func() { b.binCache = podman.NewBinaryCache(readExecutable) })
	return b.binCache.Bytes(encoding)
}

func (b *Boxes) binarySum() (string, error) {
	b.binOnce.Do(func() { b.binSum, b.binErr = executableSum() })
	return b.binSum, b.binErr
}

func executableSum() (string, error) {
	binary, err := readExecutable()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(binary)
	return hex.EncodeToString(sum[:]), nil
}

func readExecutable() ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(executable)
}

// installHost stages once, but verifies and repairs activation before each start; immutable pins make later link drift harmless.
func (b *Boxes) installHost(ctx context.Context, name contract.HostName) error {
	b.mu.Lock()
	installed := b.installed[name]
	b.mu.Unlock()
	host, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	if installed {
		wanted, err := b.binarySum()
		if err != nil {
			return err
		}
		step, cancel := context.WithTimeout(ctx, box.StartStepBudget)
		defer cancel()
		err = host.ActivateBinary(step, b.Layout.HostBin(), wanted)
		if err != nil && ctx.Err() == nil && errors.Is(step.Err(), context.DeadlineExceeded) {
			return errors.Join(podman.ErrTransferIdle, err)
		}
		return err
	}
	if err := b.installBinary(ctx, host); err != nil {
		return err
	}
	b.mu.Lock()
	if b.installed == nil {
		b.installed = make(map[contract.HostName]bool)
	}
	b.installed[name] = true
	b.mu.Unlock()
	return nil
}
