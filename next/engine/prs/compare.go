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

// reading is what PRs and issues compare alike in one poll of one item. open means the item stays in the poll, so what is
// held back has a later pass to arrive on.
type reading struct {
	viewer  string
	now     time.Time
	open    bool
	waiting bool // the token's own account woke a session inside ownWakeGap
	fresh   []Comment
	ownNews bool
}

func withinGap(at int64, now time.Time) bool { return now.Before(time.UnixMilli(at).Add(ownWakeGap)) }

func newReading(viewer string, now time.Time, open bool, ownWakeAt int64) *reading {
	return &reading{viewer: viewer, now: now, open: open, waiting: open && withinGap(ownWakeAt, now)}
}

// take moves a cursor past new comments, and stops at a comment that must wait.
func (r *reading) take(all []Comment, cursor *int64) {
	for _, item := range all {
		if item.ID <= *cursor {
			continue
		}
		own := item.Author == r.viewer
		switch {
		case item.Body == "" || own && agentWrote(item.Body):
		case own && r.waiting:
			return
		default:
			r.fresh = append(r.fresh, item)
			r.ownNews = r.ownNews || own
		}
		*cursor = item.ID
	}
}

// comments returns what take found, capped, and how many more there were.
func (r *reading) comments() ([]contract.PrComment, int) {
	var out []contract.PrComment
	for _, item := range r.fresh[:min(len(r.fresh), maxComments)] {
		out = append(out, contract.PrComment{Author: item.Author, Body: shorten(item.Body), URL: item.URL, Outside: item.Author != r.viewer})
	}
	return out, max(len(r.fresh)-maxComments, 0)
}

// meta compares metadata with what the session was last told. The first read has nothing to compare with, so it records
// what the item shows. Labels churn, so a change with nothing else to tell wakes at most once per item in ownWakeGap: inside it
// the told metadata stays, so the change waits and arrives on the first poll after, or when the item leaves the poll. Other
// news on the item carries it at once, without spending the window. It returns the metadata to store, what to tell, and the
// wake time to store.
func (r *reading) meta(told *store.ItemMeta, seen store.ItemMeta, wokeAt int64, news bool) (*store.ItemMeta, *contract.MetaChange, int64) {
	if told == nil {
		return &seen, nil, wokeAt
	}
	change := diffMeta(*told, seen)
	if change == nil || !news && r.open && withinGap(wokeAt, r.now) {
		return told, nil, wokeAt
	}
	if !news {
		wokeAt = r.now.UnixMilli()
	}
	return &seen, change, wokeAt
}

// diffMeta says what moved, or nil if nothing did.
func diffMeta(told, seen store.ItemMeta) *contract.MetaChange {
	change := contract.MetaChange{
		BodyEdited: seen.EditedAt != told.EditedAt,
		Reopened:   seen.Reopened > told.Reopened,
		Draft:      seen.Draft && !told.Draft,
		Ready:      told.Draft && !seen.Draft,
	}
	if seen.Title != told.Title {
		change.Title = seen.Title
	}
	if seen.Milestone != told.Milestone {
		change.Milestone = seen.Milestone
		change.MilestoneRemoved = seen.Milestone == ""
	}
	change.LabelsAdded, change.LabelsRemoved = diffNames(told.Labels, seen.Labels)
	change.Assigned, change.Unassigned = diffNames(told.Assignees, seen.Assignees)
	change.ReviewersAdded, change.ReviewersRemoved = diffNames(told.Reviewers, seen.Reviewers)
	if reflect.DeepEqual(change, contract.MetaChange{}) {
		return nil
	}
	return &change
}

func diffNames(told, seen []string) (added, removed []string) {
	for _, name := range seen {
		if !slices.Contains(told, name) {
			added = append(added, name)
		}
	}
	for _, name := range told {
		if !slices.Contains(seen, name) {
			removed = append(removed, name)
		}
	}
	return added, removed
}

// Compare turns one poll of one PR into what to store and who to tell. It reports whether anything moved, and with it
// whether the poll backs off. The stored row is what makes a change news once, across restarts too.
//
// A comment by the token's own account may be a session's, so answering one must not start a loop. That account also
// speaks for the owner, so it is not ignored: only text with the agent marker is, and the rest wakes at most once per PR in
// ownWakeGap. A comment inside the gap waits, with its cursor unmoved, so the owner's words arrive late rather than never.
func Compare(row store.TrackedPR, seen Observed, viewer string, now time.Time) (store.PRUpdate, bool) {
	next := row.PrSummary
	next.URL, next.Title, next.Author, next.Draft, next.State, next.Head, next.Review, next.CI = seen.URL, seen.Title, seen.Author, seen.Draft, seen.State, seen.Head, seen.Review, seen.CI
	// GitHub works mergeability out on demand, and until then the last answer stands.
	if seen.Merge != "" {
		next.Merge = seen.Merge
	}
	update := store.PRUpdate{PR: next, HeadRepo: seen.HeadRepo, CITold: row.CITold, Seen: row.Seen, OwnWakeAt: row.OwnWakeAt}

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

	// A PR leaving the poll has no later pass to deliver a waiting comment.
	read := newReading(viewer, now, seen.State == contract.PrOpen, row.OwnWakeAt)
	read.take(seen.Reviews, &update.Seen.Reviews)
	read.take(seen.ReviewComments, &update.Seen.ReviewComments)
	read.take(seen.Comments, &update.Seen.Comments)
	var meta *contract.MetaChange
	update.Meta, meta, update.MetaWakeAt = read.meta(row.Meta, seen.Meta, row.MetaWakeAt, len(kinds)+len(read.fresh) > 0)

	moved := next != row.PrSummary || seen.HeadRepo != row.HeadRepo || update.Seen != row.Seen || update.CITold != row.CITold
	// A baseline records the PR as it stands: nothing it already shows is a change.
	if row.Baseline {
		return update, true
	}
	if !moved && meta == nil {
		// Metadata read for the first time is a row to write and nothing to tell.
		return update, update.Meta != row.Meta
	}
	change := contract.PrChange{PR: next, Kinds: kinds, Outside: seen.Author != viewer, Meta: meta}
	if slices.Contains(kinds, contract.PrChangeCIFailed) {
		change.FailedChecks = seen.FailedChecks[:min(len(seen.FailedChecks), maxChecks)]
	}
	if read.ownNews {
		update.OwnWakeAt = now.UnixMilli()
	}
	if len(read.fresh) > 0 {
		change.Kinds = append(change.Kinds, contract.PrChangeComment)
		change.Comments, change.MoreComments = read.comments()
	}
	if meta != nil {
		change.Kinds = append(change.Kinds, contract.PrChangeMetadata)
	}
	update.Change = &change
	return update, true
}

// CompareIssue does for one issue what Compare does for a PR, under the same rules for comments and metadata.
func CompareIssue(row store.TrackedIssue, seen ObservedIssue, viewer string, now time.Time) (store.IssueUpdate, bool) {
	next := row.IssueSummary
	next.URL, next.Title, next.Author, next.State = seen.URL, seen.Meta.Title, seen.Author, seen.State
	update := store.IssueUpdate{Issue: next, SeenComments: row.SeenComments, OwnWakeAt: row.OwnWakeAt}

	var kinds []contract.PrChangeKind
	if seen.State == contract.IssueClosed {
		kinds = append(kinds, contract.PrChangeClosed)
	}
	read := newReading(viewer, now, seen.State == contract.IssueOpen, row.OwnWakeAt)
	read.take(seen.Comments, &update.SeenComments)
	var meta *contract.MetaChange
	update.Meta, meta, update.MetaWakeAt = read.meta(row.Meta, seen.Meta, row.MetaWakeAt, len(kinds)+len(read.fresh) > 0)

	moved := next.URL != row.URL || next.Title != row.Title || next.Author != row.Author || next.State != row.State || update.SeenComments != row.SeenComments
	if row.Baseline {
		return update, true
	}
	if !moved && meta == nil {
		return update, update.Meta != row.Meta
	}
	change := contract.IssueChange{Issue: next, Kinds: kinds, Outside: seen.Author != viewer, Meta: meta}
	if read.ownNews {
		update.OwnWakeAt = now.UnixMilli()
	}
	if len(read.fresh) > 0 {
		change.Kinds = append(change.Kinds, contract.PrChangeComment)
		change.Comments, change.MoreComments = read.comments()
	}
	if meta != nil {
		change.Kinds = append(change.Kinds, contract.PrChangeMetadata)
		change.Issue.Labels, change.Issue.Assignees, change.Issue.Milestone = seen.Meta.Labels, seen.Meta.Assignees, seen.Meta.Milestone
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
