package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	accountservice "dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/automations"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// The account-usage hook owns the usage probes, so switching it off stops them.
// Renewal stays on the account timer at its exact deadline, because a late
// renewal logs the person out, so the switch never touches it.
func TestDisabledAccountUsageHookStopsUsageButNotRenewal(t *testing.T) {
	root := storetest.Isolate(t)
	if err := stores.Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	ctx := t.Context()
	accounts := accountservice.New(accountservice.Options{Store: accountstore.DefaultStore()})
	n := notify.New(room.DefaultStore(), &subagent.Manager{Store: subagent.DefaultStore()})
	service := hooks.New(automations.Hooks(n, accounts)...)
	if err := service.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	name := string(automations.BuiltinAccountUsage)
	off := true
	if err := service.Edit(ctx, "", name, "owner", hooks.Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}

	// Two usage intervals pass with the hook off: no usage pass runs.
	now := time.Now()
	for _, at := range []time.Time{now, now.Add(5 * time.Minute), now.Add(10 * time.Minute)} {
		if err := service.TickGlobal(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	if runs, err := service.Runs(ctx, "", name); err != nil || len(runs) != 0 {
		t.Fatalf("a switched-off account-usage hook ran %d passes, %v", len(runs), err)
	}

	// Renewal keeps its own timer whatever the hook says.
	clock := newFakeClock()
	set := clock.timers(ctx)
	done := make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		refreshAccounts(runCtx, accounts.RefreshCredentials, set, hub.New(0))
	}()
	defer func() { cancel(); <-done }()
	awaitTimer(t, set, timerAccounts)

	// Switched back on, the hook runs the usage pass and logs its line.
	on := false
	if err := service.Edit(ctx, "", name, "owner", hooks.Patch{Disabled: &on}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickGlobal(ctx, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	runs, err := service.Runs(ctx, "", name)
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("usage runs = %+v, %v", runs, err)
	}
	log, err := os.ReadFile(runs[0].LogPath)
	if err != nil || !strings.Contains(string(log), "refreshed account usage") {
		t.Fatalf("usage log = %q, %v", log, err)
	}
}
