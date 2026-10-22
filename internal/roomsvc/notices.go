package roomsvc

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/subagent"
)

func (s *Service) notices() notices.Store {
	root := s.sessions().Root
	if root == "" {
		root = subagent.DefaultStore().Root
	}
	return notices.Store{Dir: filepath.Dir(root)}
}

// Notices lists the machine-wide owner notices for the home screen.
func (s *Service) Notices() ([]notices.Notice, error) { return s.notices().List() }

func (s *Service) DismissNotice(id string) error { return s.notices().Dismiss(id) }

// UpdateDyna runs `dyna update` with the session executable, so the home
// screen's Update button uses the same code path as the command.
func (s *Service) UpdateDyna(ctx context.Context) error {
	if s.Notify.Sessions.Executable == "" {
		return fmt.Errorf("dyna executable is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := childenv.Clean(exec.CommandContext(ctx, s.Notify.Sessions.Executable, "update")).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dyna update: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// scanNotices emits {kind: "notices"} when the notice revision changes.
func (s *Service) scanNotices(version *string, first bool, emit func(Event)) {
	rev, err := s.notices().Revision()
	if err != nil {
		return
	}
	current := fmt.Sprint(rev)
	if current != *version && !first {
		emit(Event{Kind: "notices"})
	}
	*version = current
}
