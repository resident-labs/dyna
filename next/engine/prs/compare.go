package prs

import (
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	maxChecks      = 5
	maxComments    = 5
	maxCommentText = 2000 // characters
	// agentMarker opens what agents write through the owner's account, after any quote marks. Horizon's two disclosure lines both start this way.
	agentMarker = "🤖"
	// ownWakeGap is the shortest gap between wakes from the token account's comments on one item, and between metadata-only wakes.
	ownWakeGap = 10 * time.Minute
)

func agentWrote(body string) bool {
	return strings.HasPrefix(strings.TrimLeft(body, "> \t\r\n"), agentMarker)
}

func withinGap(at int64, now time.Time) bool { return now.Before(time.UnixMilli(at).Add(ownWakeGap)) }

// reading sorts what a pass brought for one item, and what earlier passes held back, into what to tell now and what waits.
type reading struct {
	comments []contract.GitHubItem
	actions  []contract.GitHubItem
	held     []contract.GitHubItem
	ownNews  bool
}

// A comment by the token's own account may be a session's, so answering one must not start a loop. That account also speaks
// for the owner, so it is not ignored: only text with the agent marker is, and the rest wakes at most once per item in
// ownWakeGap. A comment inside the gap waits, so the owner's words arrive late rather than never. open means the item stays
// watched, so what is held back has a later pass to arrive on.
func sift(items []contract.GitHubItem, now time.Time, open bool, ownWakeAt int64) *reading {
	r := &reading{}
	waiting := open && withinGap(ownWakeAt, now)
	for _, item := range items {
		switch item.Action {
		case contract.GitHubOpened, contract.GitHubForcePushed, contract.GitHubMentioned: // for subscribers
		case contract.GitHubCommented, contract.GitHubReviewCommented, contract.GitHubReviewed:
			own := !item.Outside
			switch {
			case item.Text == "" || own && agentWrote(item.Text):
			case item.Action == contract.GitHubCommented && strings.HasSuffix(item.Actor, "[bot]"):
			case own && waiting:
				r.held = append(r.held, item)
			default:
				r.comments = append(r.comments, item)
				r.ownNews = r.ownNews || own
			}
		default:
			r.actions = append(r.actions, item)
		}
	}
	return r
}

// limit applies the metadata window. Labels churn, so actions with nothing else to tell wake at most once per item in
// ownWakeGap: inside it they wait, and arrive on the first pass after, or when the item leaves the watch. Other news on the
// item carries them at once, without spending the window. It returns the wake time to store.
func (r *reading) limit(news bool, open bool, wokeAt int64, now time.Time) int64 {
	switch {
	case len(r.actions) == 0 || news:
		return wokeAt
	case open && withinGap(wokeAt, now):
		r.held, r.actions = append(r.held, r.actions...), nil
		return wokeAt
	}
	return now.UnixMilli()
}

// shown returns the comments to show, capped, and how many more there were.
func (r *reading) shown() ([]contract.PrComment, int) {
	var out []contract.PrComment
	for _, item := range r.comments[:min(len(r.comments), maxComments)] {
		out = append(out, contract.PrComment{Author: item.Actor, Body: item.Text, URL: item.URL, Outside: item.Outside})
	}
	return out, max(len(r.comments)-maxComments, 0)
}

// kinds adds what the comments and actions make news to what a read found. An action that is a kind already, as a merge is, adds nothing.
func (r *reading) kinds(read []contract.PrChangeKind) []contract.PrChangeKind {
	if len(r.comments) > 0 {
		read = append(read, contract.PrChangeComment)
	}
	if slices.ContainsFunc(r.actions, func(item contract.GitHubItem) bool { return !slices.Contains(read, contract.PrChangeKind(item.Action)) }) {
		read = append(read, contract.PrChangeMetadata)
	}
	return read
}

// refresh takes a feed's view of an item, and tells an edit by a body hash that differs from one already held.
func refresh(meta *store.ItemMeta, listed *FeedItem, item contract.GitHubItem) []contract.GitHubItem {
	var edits []contract.GitHubItem
	if meta.Body != "" && meta.Body != listed.Meta.Body {
		item.Number, item.Kind, item.Action, item.URL = listed.Number, listed.Kind, contract.GitHubEdited, listed.URL
		edits = append(edits, item)
	}
	meta.Labels, meta.Assignees, meta.Milestone, meta.Body = listed.Meta.Labels, listed.Meta.Assignees, listed.Meta.Milestone, listed.Meta.Body
	return edits
}

// noteWriter remembers an outside login that set free text, so the item's text reads as theirs however it is reached.
func noteWriter(meta *store.ItemMeta, items []contract.GitHubItem) {
	for _, item := range items {
		switch item.Action {
		case contract.GitHubRenamed, contract.GitHubLabeled, contract.GitHubMilestoned:
			if item.Outside {
				meta.EditedBy = item.Actor
			}
		}
	}
}

// settle sorts an item's items under the wake limits and puts what waits in meta. tell says there is something to tell.
func settle(items []contract.GitHubItem, meta *store.ItemMeta, kinds int, open bool, ownWakeAt int64, wokeAt *int64, now time.Time) (*reading, bool) {
	found := sift(append(slices.Clone(meta.Waiting), items...), now, open, ownWakeAt)
	*wokeAt = found.limit(kinds+len(found.comments) > 0, open, *wokeAt, now)
	meta.Waiting = found.held
	return found, len(found.comments)+len(found.actions) > 0
}

// ComparePR turns what one pass found on one PR into what to store and who to tell, and reports whether there is anything to
// write. listed is the PR as a feed showed it this pass, and seen as a query read it, each nil if there was neither. items are
// the feeds' records of it, oldest first. The stored row is what makes a read's change news once, across restarts too.
func ComparePR(row store.TrackedPR, items []contract.GitHubItem, listed *FeedItem, seen *Observed, viewer string, now time.Time) (store.PRUpdate, bool) {
	next, meta := row.PrSummary, row.Meta
	update := store.PRUpdate{Was: row.State, HeadRepo: row.HeadRepo, CITold: row.CITold, SeenReviews: row.SeenReviews, OwnWakeAt: row.OwnWakeAt, MetaWakeAt: row.MetaWakeAt}
	if listed != nil {
		next.URL, next.Title, next.Author, next.Draft = listed.URL, listed.Title, listed.Author, listed.Draft
		items = append(slices.Clone(items), refresh(&meta, listed, contract.GitHubItem{Repo: row.Repo, Author: listed.Author})...)
	}
	var kinds []contract.PrChangeKind
	var failed []string
	if seen != nil {
		next.State, next.Head, next.Review, next.CI = seen.State, seen.Head, seen.Review, seen.CI
		update.HeadRepo = seen.HeadRepo
		// GitHub works mergeability out on demand, and until then the last answer stands.
		if seen.Merge != "" {
			next.Merge = seen.Merge
		}
		meta.Recheck = seen.Merge == ""
		if seen.Head != row.Head {
			meta.HeadAt = now.UnixMilli()
		}
		for _, review := range seen.Reviews {
			if review.ID > update.SeenReviews {
				update.SeenReviews = review.ID
				items = append(items, contract.GitHubItem{Repo: row.Repo, Number: row.Number, Kind: contract.GitHubPR, Action: contract.GitHubReviewed, Actor: review.Author, URL: review.URL, Text: shorten(review.Body), Outside: review.Author != viewer})
			}
		}
	}
	// An event is at least as new as the read beside it, which can lag it, and nothing would read this PR again to catch up.
	for _, item := range items {
		switch item.Action {
		case contract.GitHubMerged:
			next.State = contract.PrMerged
		case contract.GitHubClosed:
			next.State = contract.PrClosed
		case contract.GitHubReopened:
			next.State = contract.PrOpen
		}
	}
	if seen != nil && seen.State == contract.PrMerged {
		next.State = contract.PrMerged
	}
	switch {
	case next.State == contract.PrMerged && row.State != contract.PrMerged:
		kinds = append(kinds, contract.PrChangeMerged)
	case next.State == contract.PrClosed && row.State != contract.PrClosed:
		kinds = append(kinds, contract.PrChangeClosed)
	case next.State == contract.PrOpen && seen != nil:
		// A result is news once per head, so a rerun that ends the same way stays quiet.
		told := seen.Head + ":" + string(seen.CI)
		if (seen.CI == contract.PrCISuccess || seen.CI == contract.PrCIFailure) && told != row.CITold {
			update.CITold = told
			if seen.CI == contract.PrCIFailure {
				kinds = append(kinds, contract.PrChangeCIFailed)
				failed = seen.FailedChecks[:min(len(seen.FailedChecks), maxChecks)]
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
	noteWriter(&meta, items)
	update.PR, update.Meta = next, meta
	// A baseline records the PR as it stands: nothing it already shows is a change.
	if row.Baseline {
		update.Meta.Waiting = nil
		return update, true
	}
	found, tell := settle(items, &update.Meta, len(kinds), next.State == contract.PrOpen, row.OwnWakeAt, &update.MetaWakeAt, now)
	if next == row.PrSummary && update.HeadRepo == row.HeadRepo && update.CITold == row.CITold && !tell {
		return update, update.SeenReviews != row.SeenReviews || !reflect.DeepEqual(update.Meta, row.Meta)
	}
	change := contract.PrChange{PR: next, Kinds: found.kinds(kinds), FailedChecks: failed, Outside: next.Author != "" && next.Author != viewer && update.HeadRepo != row.Repo, Actions: found.actions}
	// A first read has no head to move from. A push is told to subscribers only, and spends no wake window.
	if row.Head != "" && next.Head != row.Head && next.State == contract.PrOpen {
		change.Kinds = append(change.Kinds, contract.PrChangePushed)
		change.Push = &contract.PrPush{Before: row.Head, After: next.Head}
	}
	change.Comments, change.MoreComments = found.shown()
	if found.ownNews {
		update.OwnWakeAt = now.UnixMilli()
	}
	update.Change = &change
	return update, true
}

// CompareIssue does for one issue what ComparePR does for a PR, under the same rules. A feed's events move its state, and state
// is what a query read when the feeds lost their place, or empty.
func CompareIssue(row store.TrackedIssue, items []contract.GitHubItem, listed *FeedItem, state contract.IssueState, viewer string, now time.Time) (store.IssueUpdate, bool) {
	next, meta := row.IssueSummary, row.Meta
	update := store.IssueUpdate{Was: row.State, OwnWakeAt: row.OwnWakeAt, MetaWakeAt: row.MetaWakeAt}
	if listed != nil {
		next.URL, next.Title, next.Author = listed.URL, listed.Title, listed.Author
		items = append(slices.Clone(items), refresh(&meta, listed, contract.GitHubItem{Repo: row.Repo, Author: listed.Author})...)
	}
	for _, item := range items {
		switch item.Action {
		case contract.GitHubClosed:
			next.State = contract.IssueClosed
		case contract.GitHubReopened:
			next.State = contract.IssueOpen
		}
	}
	if state != "" {
		next.State = state
	}
	next.Labels, next.Assignees, next.Milestone = meta.Labels, meta.Assignees, meta.Milestone
	noteWriter(&meta, items)
	update.Issue, update.Meta = next, meta
	if row.Baseline {
		update.Meta.Waiting = nil
		return update, true
	}
	var kinds []contract.PrChangeKind
	if next.State == contract.IssueClosed && row.State != contract.IssueClosed {
		kinds = append(kinds, contract.PrChangeClosed)
	}
	found, tell := settle(items, &update.Meta, len(kinds), next.State == contract.IssueOpen, row.OwnWakeAt, &update.MetaWakeAt, now)
	if reflect.DeepEqual(next, row.IssueSummary) && !tell {
		return update, !reflect.DeepEqual(update.Meta, row.Meta)
	}
	change := contract.IssueChange{Issue: next, Kinds: found.kinds(kinds), Outside: next.Author != viewer, Actions: found.actions}
	change.Comments, change.MoreComments = found.shown()
	if found.ownNews {
		update.OwnWakeAt = now.UnixMilli()
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
