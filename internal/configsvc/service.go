package configsvc

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// Service owns the process settings used by live profile checks and update checks.
type Service struct {
	CLI      string
	NoUpdate bool
	ReadOnly bool
	// Sessions is the engine's Manager, which notes profile changes on its
	// sessions. Nil uses a fresh Manager.
	Sessions *subagent.Manager
}

// ConfigCheckProfile permits observed checks in read-only mode, but refuses live probes.
func (s *Service) ConfigCheckProfile(ctx context.Context, name string, live bool) (ProfileCheck, error) {
	if live && s.ReadOnly {
		return ProfileCheck{}, websvc.ErrReadOnly
	}
	return CheckProfile(ctx, name, live)
}

// UpdateNotice checks the CLI version and uses the shared daily update cache.
// It never installs an update.
func (s *Service) UpdateNotice(ctx context.Context) UpdateNotice {
	if s.CLI == "" {
		return UpdateNotice{}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := childenv.Clean(exec.CommandContext(ctx, s.CLI, "version")).Output()
	if err != nil {
		return UpdateNotice{}
	}
	version := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "dyna "))
	n, _ := CheckUpdate(ctx, version, s.NoUpdate)
	return n
}
