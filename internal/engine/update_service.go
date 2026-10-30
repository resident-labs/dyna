package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/updaterservice"
)

type UpdateResult string

const (
	UpdatePending UpdateResult = "pending"
	UpdatePassed  UpdateResult = "pass"
	UpdateFailed  UpdateResult = "error"
)

// UpdateRun is the owner-facing summary of one installer receipt.
type UpdateRun struct {
	ID         string       `json:"id"`
	Result     UpdateResult `json:"result"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt,omitempty"`
}

func updateRuns() ([]UpdateRun, error) {
	jobs, err := hookinstall.DefaultStore().Recent(updaterservice.RunName)
	if err != nil {
		return nil, err
	}
	out := []UpdateRun{}
	for _, j := range jobs {
		result := UpdatePending
		if j.Status == "finished" {
			result = UpdateFailed
			if j.Result == "pass" {
				result = UpdatePassed
			}
		}
		out = append(out, UpdateRun{ID: j.ID, Result: result, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt})
	}
	return out, nil
}

func updateRunLog(id string) (hooks.HookRunLog, error) {
	j, err := hookinstall.DefaultStore().Read(id)
	if err != nil {
		return hooks.HookRunLog{}, err
	}
	if j.Hook != updaterservice.RunName {
		return hooks.HookRunLog{}, fmt.Errorf("unknown update run")
	}
	f, err := os.Open(j.HookLog)
	if err != nil {
		return hooks.HookRunLog{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return hooks.HookRunLog{}, err
	}
	const max = 8 << 10
	if info.Size() > max {
		if _, err := f.Seek(info.Size()-max, io.SeekStart); err != nil {
			return hooks.HookRunLog{}, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return hooks.HookRunLog{}, err
	}
	code := -1
	if j.Result == "pass" {
		code = 0
	}
	return hooks.HookRunLog{Tail: string(b), ExitCode: code}, nil
}

func updateSourceItem(source string) configsvc.Item {
	origin := settings.SourceDefault
	if source != "" {
		origin = settings.SourceFile
	}
	return configsvc.Item{Key: "updater.source", Value: source, Source: origin, LiveVsRestart: settings.LiveNextOperation, Kind: "string", Help: "Absolute source checkout for automatic source updates. Empty keeps release updates unchanged."}
}

func (s services) settingsItems(ctx context.Context) ([]configsvc.Item, error) {
	items, err := configsvc.Items()
	if err != nil {
		return nil, err
	}
	cfg, err := (updaterservice.Store{DataDir: store.DataDir()}).Get(ctx)
	if err != nil {
		return nil, err
	}
	return append(items, updateSourceItem(cfg.Source)), nil
}

func editUpdate(ctx context.Context, patch hooks.ServicePatch) error {
	if patch.Timeout != nil {
		return fmt.Errorf("the Updates installer has a fixed 75-minute limit")
	}
	if patch.EveryMinutes != nil && *patch.EveryMinutes < 1 {
		return fmt.Errorf("interval must be at least one minute")
	}
	return (updaterservice.Store{DataDir: store.DataDir()}).Update(ctx, func(c *updaterservice.Config) error {
		if patch.Disabled != nil {
			if !*patch.Disabled && c.Source == "" {
				return fmt.Errorf("set updater.source before enabling Updates")
			}
			c.Enabled = !*patch.Disabled
		}
		if patch.EveryMinutes != nil {
			c.EveryMinutes = *patch.EveryMinutes
		}
		return nil
	})
}

func scheduledUpdate(ctx context.Context, h hooks.Service, now time.Time) error {
	cfg, due, err := (updaterservice.Store{DataDir: store.DataDir()}).ClaimInterval(ctx, now)
	if err != nil || !due {
		return err
	}
	_, err = runUpdate(ctx, h, cfg.Source, cfg.ToolPath)
	return err
}

func runUpdate(ctx context.Context, h hooks.Service, source, toolPath string) (hooks.Run, error) {
	if source == "" {
		return hooks.Run{}, fmt.Errorf("set updater.source before running Updates")
	}
	r, err := repoid.Resolve(ctx, source)
	if err != nil {
		return hooks.Run{}, err
	}
	branch, err := repoid.DefaultBranch(ctx, r.Root, repoid.Git)
	if err != nil {
		return hooks.Run{}, err
	}
	sha, err := repoid.Git(ctx, r.Root, "rev-parse", "--verify", "origin/"+branch.Name+"^{commit}")
	if err != nil {
		return hooks.Run{}, err
	}
	return h.StartUpdate(ctx, hooks.Event{Event: hooks.Schedule, Repo: r.Root, Worktree: r.Main, Branch: branch.Name, SHA: sha}, source, toolPath)
}
