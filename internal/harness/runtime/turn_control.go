package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"time"
)

// StopTurns stops native process groups, including workflow workers. The caller
// must own a pending switch, which keeps new harness admission closed.
func (a *Accounts) StopTurns(ctx context.Context, harness string) error {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return err
	}
	pending, _ := a.SwitchStatus(h)
	if !pending {
		return errors.New("Start an account switch before stopping its turns.")
	}
	root := filepath.Join(a.Root, h, ".turns")
	signaled := map[string]time.Time{}
	forced := map[string]bool{}
	for {
		paths, err := resourceLeasePaths(root, "")
		if err != nil {
			return err
		}
		remaining := 0
		for _, path := range paths {
			live, use, e := resourceLeaseState(path)
			if e != nil {
				return e
			}
			if !live {
				removeResourceLease(path)
				continue
			}
			remaining++
			if len(use.Reference) == 0 {
				continue
			}
			key := string(use.Reference)
			sig := syscall.SIGTERM
			if sent, ok := signaled[key]; ok {
				if forced[key] || time.Since(sent) < 2*time.Second {
					continue
				}
				sig = syscall.SIGKILL
				forced[key] = true
			} else {
				signaled[key] = time.Now()
			}
			if e = nativeUseSignal(use.Reference, sig); e != nil {
				alive, probeErr := nativeUseProbe(use.Reference)
				if alive || probeErr != nil {
					return errors.New("A native turn could not be stopped; the account switch remains pending.")
				}
			}
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
