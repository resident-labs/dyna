package prs

import (
	"slices"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	maxChecks      = 5
	maxComments    = 5
	maxCommentText = 2000 // characters
)

// Compare turns one poll of one PR into what to store and who to tell. It reports whether anything moved, and with it
// whether the poll backs off. The stored row is what makes a change news once, across restarts too.
func Compare(row store.TrackedPR, seen Observed, viewer string) (store.PRUpdate, bool) {
	next := row.PrSummary
	next.URL, next.Title, next.Author, next.Draft, next.State, next.Head, next.Review, next.CI = seen.URL, seen.Title, seen.Author, seen.Draft, seen.State, seen.Head, seen.Review, seen.CI
	// GitHub works mergeability out on demand, and until then the last answer stands.
	if seen.Merge != "" {
		next.Merge = seen.Merge
	}
	update := store.PRUpdate{PR: next, HeadRepo: seen.HeadRepo, CITold: row.CITold, Seen: row.Seen}

	var kinds []contract.PrChangeKind
	switch {
	case seen.State == contract.PrMerged:
		kinds = append(kinds, contract.PrChangeMerged)
	case seen.State == contract.PrClosed:
		kinds = append(kinds, contract.PrChangeClosed)
	default:
		// A result is news once per head, so a rerun that ends the same way stays quiet.
		told := seen.Head + ":" + string(seen.CI)
		if (seen.CI == contract.PrCISuccess || seen.CI == contract.PrCIFailure) && told != row.CITold {
			update.CITold = told
			if seen.CI == contract.PrCIFailure {
				kinds = append(kinds, contract.PrChangeCIFailed)
			} else {
				kinds = append(kinds, contract.PrChangeCIPassed)
			}
		}
		if seen.Review != row.Review && seen.Review == contract.PrChangesRequested {
			kinds = append(kinds, contract.PrChangeChangesRequested)
		}
		if seen.Review != row.Review && seen.Review == contract.PrApproved {
			kinds = append(kinds, contract.PrChangeApproved)
		}
		if next.Merge != row.Merge && next.Merge == contract.PrMergeConflict {
			kinds = append(kinds, contract.PrChangeConflict)
		}
	}

	var fresh []Comment
	take := func(all []Comment, cursor *int64) {
		for _, item := range all {
			if item.ID <= *cursor {
				continue
			}
			*cursor = item.ID
			if item.Body != "" {
				fresh = append(fresh, item)
			}
		}
	}
	take(seen.Reviews, &update.Seen.Reviews)
	take(seen.ReviewComments, &update.Seen.ReviewComments)
	take(seen.Comments, &update.Seen.Comments)

	moved := next != row.PrSummary || seen.HeadRepo != row.HeadRepo || update.Seen != row.Seen || update.CITold != row.CITold
	// A baseline records the PR as it stands: nothing it already shows is a change.
	if row.Baseline {
		return update, true
	}
	if !moved {
		return update, false
	}
	change := contract.PrChange{PR: next, Kinds: kinds, Outside: seen.Author != viewer}
	if slices.Contains(kinds, contract.PrChangeCIFailed) {
		change.FailedChecks = seen.FailedChecks[:min(len(seen.FailedChecks), maxChecks)]
	}
	if len(fresh) > 0 {
		change.Kinds = append(change.Kinds, contract.PrChangeComment)
		for _, item := range fresh[:min(len(fresh), maxComments)] {
			change.Comments = append(change.Comments, contract.PrComment{Author: item.Author, Body: shorten(item.Body), URL: item.URL, Outside: item.Author != viewer})
		}
		change.MoreComments = max(len(fresh)-maxComments, 0)
	}
	update.Change = &change
	return update, true
}

func shorten(text string) string {
	if utf8.RuneCountInString(text) <= maxCommentText {
		return text
	}
	return string([]rune(text)[:maxCommentText]) + "…"
}
