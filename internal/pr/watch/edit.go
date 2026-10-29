package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
)

// PREdit names the pull request fields to replace. A nil field stays unchanged.
type PREdit struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
}

// Edit changes a tracked pull request through the room's GitHub credential. It
// runs gh pr edit, which policy admits for a fork's parent repository, so a
// fork-head upstream PR can be edited without a raw API write.
func (s *Service) Edit(ctx context.Context, key string, edit PREdit) error {
	if edit.Title == nil && edit.Body == nil {
		return fmt.Errorf("a PR edit needs a title or body")
	}
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	// An edit by a session, a worker or the root carries agent text, which the gh
	// policy checks. The owner's own edit passes unchanged.
	if authority.Of(ctx).Role != authority.Owner {
		ctx = ghclient.WithAgentText(ctx)
	}
	ref := refRecord(r)
	args := []string{"pr", "edit", ref.URL()}
	if edit.Title != nil {
		args = append(args, "--title", publicationBody(*edit.Title))
	}
	ctx, cancel := context.WithTimeout(ctx, s.GH.requestTimeout())
	defer cancel()
	unlock, err := s.publicationGate(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	if edit.Body != nil {
		dir, err := os.MkdirTemp(s.Store.Root, "edit-body-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "body.md")
		if err := os.WriteFile(file, []byte(Sanitize(*edit.Body)), 0600); err != nil {
			return err
		}
		args = append(args, "--body-file", file)
	}
	_, err = s.githubFor(ref).command(ctx, "", false, args...)
	return err
}
