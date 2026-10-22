package watch

import (
	"context"
	"os"
	"path/filepath"

	"dyna-agent/internal/pr/state"
)

// TODO: legacy/shim remove later — close advisory sessions and discard their frozen inputs when old batches resume or are canceled; remove after all pre-upgrade batches are gone.
func (s *Service) clearLegacyMergeReview(ctx context.Context, b *state.MergeTrial) error {
	if b.Phase != "reviewing" && b.LegacyReview == nil {
		return nil
	}
	if b.LegacyReview != nil && b.LegacyReview.Session != "" {
		if err := s.closeReviewer(ctx, b.LegacyReview.Session); err != nil {
			return err
		}
	}
	directory := filepath.Join(s.Store.Root, "trials", "review-"+filepath.Base(trialIdentity(*b)))
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	b.LegacyReview = nil
	if b.Phase == "reviewing" {
		b.Phase, b.Reason = "building", ""
	}
	return nil
}
