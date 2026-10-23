// Package automations holds the one table of automatic hooks. The engine seeds
// a global hook for each entry, the Hooks screen lists them, and
// `dyna git hooks run <name> --global` runs the same function by hand, so a new
// automatic hook is one table entry and its function.
package automations

import (
	"context"
	"fmt"
	"io"
	"time"

	accountservice "dyna-agent/internal/account/service"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/settings"
)

// The automatic hooks of this release.
const (
	// BuiltinMaintenance frees disk space and removes stale trees and runs.
	BuiltinMaintenance hooks.Builtin = "maintenance"
	// BuiltinParentCheckIns posts the periodic report of each parent's children.
	BuiltinParentCheckIns hooks.Builtin = "parent-checkins"
	// BuiltinHarnessVersions keeps the recommended harness versions fresh.
	BuiltinHarnessVersions hooks.Builtin = "harness-versions"
	// BuiltinAccountUsage refreshes the saved usage readings of the accounts.
	BuiltinAccountUsage hooks.Builtin = "account-usage"
)

// defaultCheckInMinutes is a parent's first check-in spacing until the owner
// sets the hook's own interval.
const defaultCheckInMinutes = 15

// usageCheckMinutes is the shortest usage cadence the account timer used: an
// active harness with running turns re-reads its reading every five minutes.
// The pass still probes each account only when its own deadline is due.
const usageCheckMinutes = 5

// Hooks returns the automatic hooks of this release. n is the engine's notify
// service, so a pass posts through the engine's rooms and reads the engine's
// sessions instead of building its own. accounts runs the usage probes.
func Hooks(n *notify.Service, accounts *accountservice.Service) []hooks.BuiltinSpec {
	return []hooks.BuiltinSpec{
		{
			Builtin:      BuiltinMaintenance,
			Label:        "Maintenance",
			EveryMinutes: 60,
			Timeout:      "30m",
			Run:          maintenancePass(n),
		},
		{
			Builtin:      BuiltinParentCheckIns,
			Label:        "Parent check-ins",
			EveryMinutes: checkInMinutes(),
			Timeout:      "5m",
			Run:          checkInPass(n),
		},
		{
			Builtin:      BuiltinHarnessVersions,
			Label:        "Harness version checks",
			EveryMinutes: int(harnessruntime.VersionCheckInterval / time.Minute),
			Timeout:      "2m",
			Run:          harnessVersionsPass,
		},
		{
			Builtin:      BuiltinAccountUsage,
			Label:        "Account usage",
			EveryMinutes: usageCheckMinutes,
			Timeout:      "2m",
			Run:          accountUsagePass(accounts),
		},
	}
}

// maintenancePass frees disk space and removes stale worktrees, scratch and
// runs. Disk-pressure cleanup stays in the engine loop, which ignores the
// switch on this hook.
func maintenancePass(n *notify.Service) func(context.Context, int, io.Writer) error {
	return func(ctx context.Context, _ int, log io.Writer) error {
		_, err := maintenance.Pass(ctx, n.Sessions.RetainClosedSession, logger(log))
		return err
	}
}

// checkInPass posts every parent check-in that is due, spaced by the hook's own
// interval, and writes the posted count to the run log. The spacing keeps a
// minute of slack: the runner starts a hook a moment after its interval ends,
// and a check-in due exactly at the run must not wait a whole extra interval.
func checkInPass(n *notify.Service) func(context.Context, int, io.Writer) error {
	return func(ctx context.Context, minutes int, log io.Writer) error {
		s := parentcheckin.New(n)
		s.Interval = func() (time.Duration, error) {
			return time.Duration(max(1, minutes-1)) * time.Minute, nil
		}
		posted, _, err := s.Pass(ctx)
		if _, writeErr := fmt.Fprintf(log, "posted %d check-ins\n", posted); writeErr != nil {
			return writeErr
		}
		return err
	}
}

// checkInMinutes is the interval a new parent check-ins row starts with. The
// hook owns the interval now, so the sessions.checkInInterval setting that fed
// the engine's own timer is imported once, for a row that does not exist yet.
// TODO: legacy/shim remove later — reads the replaced check-in interval once for the seeded row; remove with the settings key.
func checkInMinutes() int {
	cfg, err := settings.Load("")
	if err != nil || cfg.SessionCheckInInterval < time.Minute {
		return defaultCheckInMinutes
	}
	return int(cfg.SessionCheckInInterval / time.Minute)
}

// harnessVersionsPass reads every harness's recommended version into the
// engine's cache, which the install and update views read.
func harnessVersionsPass(ctx context.Context, _ int, log io.Writer) error {
	versions := harnessruntime.CheckVersions(ctx)
	if _, err := fmt.Fprintf(log, "checked %d harness versions\n", len(versions)); err != nil {
		return err
	}
	return nil
}

// accountUsagePass probes the saved usage readings: inactive grants once their
// deadline passes, and the active account of a harness with running turns. It
// never renews a credential: renewal must run at the exact expiry deadline,
// because a delayed renewal logs the person out, so the engine keeps it.
func accountUsagePass(accounts *accountservice.Service) func(context.Context, int, io.Writer) error {
	return func(ctx context.Context, _ int, log io.Writer) error {
		next := accounts.RefreshInactiveUsage(ctx, time.Now())
		_, err := fmt.Fprintf(log, "refreshed account usage; next probe at %s\n", next.UTC().Format(time.RFC3339))
		return err
	}
}

// logger writes a pass's log lines to the hook run log, one line each.
func logger(out io.Writer) func(string, ...any) {
	return func(format string, args ...any) {
		_, _ = fmt.Fprintf(out, format+"\n", args...)
	}
}
