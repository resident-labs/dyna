package watch

import (
	"context"
	"fmt"
)

// PREdit names the pull request fields to replace. A nil field stays unchanged.
type PREdit struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
}

// Edit changes a tracked pull request through the room's GitHub credential.
func (s *Service) Edit(ctx context.Context, key string, edit PREdit) error {
	if edit.Title == nil && edit.Body == nil {
		return fmt.Errorf("a PR edit needs a title or body")
	}
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	ref := refRecord(r)
	args := []string{"api", "--hostname", ref.Host, ref.path(""), "--method", "PATCH"}
	if edit.Title != nil {
		args = append(args, "--raw-field", "title="+publicationBody(*edit.Title))
	}
	if edit.Body != nil {
		args = append(args, "--raw-field", "body="+Sanitize(*edit.Body))
	}
	ctx, cancel := context.WithTimeout(ctx, s.GH.requestTimeout())
	defer cancel()
	unlock, err := s.publicationGate(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = s.githubFor(ref).command(ctx, "", false, args...)
	return err
}
