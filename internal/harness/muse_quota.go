package harness

import (
	"context"
	"os"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// Capture before each process starts. Recheck after it stops so an account
// switch cannot attach the old process's error to the new sign-in. The
// execution value belongs to this process and stays fixed in its callback.
func captureMuseQuota(ctx context.Context, p profile.Profile, execution callspec.ExecutionRef, outbox *engineclient.Outbox) func(string, string) {
	noop := func(string, string) {}
	if p.Harness != profile.HarnessMuse || outbox == nil || execution.AdmissionID == "" || execution.Validate() != nil {
		return noop
	}
	host := native.Host{Env: func(key string) string {
		if value, ok := p.Env[key]; ok {
			return value
		}
		return os.Getenv(key)
	}}
	if home := host.Getenv("HOME"); home != "" {
		host.Home = home
	}
	manager, ok := native.For("muse", host)
	if !ok {
		return noop
	}
	before, err := manager.Capture(ctx, "")
	if err != nil || len(before) != 1 {
		return noop
	}
	return func(stdout, stderr string) {
		reset, ok := workeroutput.MuseQuotaReset(stdout, stderr)
		now := time.Now().UTC()
		if !ok || !reset.After(now) {
			return
		}
		// Cleanup still runs when the turn context was canceled.
		checkCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		after, err := manager.Capture(checkCtx, "")
		if err != nil || len(after) != 1 || before[0].Identity != after[0].Identity {
			return
		}
		outbox.Send("ObserveMuseQuota", callspec.MuseQuotaObservation{ExecutionRef: execution, Identity: before[0].Identity, ObservedAt: now, ResetsAt: reset})
	}
}
