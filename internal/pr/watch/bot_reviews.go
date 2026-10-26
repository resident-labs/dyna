package watch

import (
	"fmt"
	"sort"
	"strings"

	"dyna-agent/internal/pr/state"
)

// BotReviews summarizes feedback independently of the CI gate. Checks and
// statuses belong to the fetched head; reviews and inline comments name theirs.
func (s Snapshot) BotReviews(requiredChecks ...string) []state.BotReview {
	type feedback struct {
		state.BotReview
		pending, failed bool
		hasCheck        bool
		review          Comment
		link            Comment
	}
	bots := map[string]*feedback{}
	get := func(key string) *feedback {
		if bots[key] == nil {
			bots[key] = &feedback{BotReview: state.BotReview{Bot: key, State: "commented"}}
		}
		return bots[key]
	}
	// Publisher identity prevents unrelated apps with the same check name from
	// replacing each other. IDs order attempts within one publisher and name.
	checks := map[string]Check{}
	for _, c := range s.Checks {
		if ciCheck(c, requiredChecks) {
			continue
		}
		publisher := botCheckIdentity(c)
		key := fmt.Sprintf("%d/%s/%s", c.App.ID, publisher, c.Name)
		if old, ok := checks[key]; !ok || c.ID > old.ID {
			checks[key] = c
		}
	}
	for _, c := range checks {
		b := get(botCheckIdentity(c))
		b.hasCheck = true
		b.pending = b.pending || c.Status != "completed" || c.Conclusion == ""
		b.failed = b.failed || (c.Status == "completed" && c.Conclusion != "" && !success(c.Conclusion))
	}
	statuses := map[string]Status{}
	for _, status := range s.Statuses {
		if requiredCheck(status.Context, requiredChecks) {
			continue
		}
		if old, ok := statuses[status.Context]; !ok || status.ID > old.ID {
			statuses[status.Context] = status
		}
	}
	for _, status := range statuses {
		key := checkBotKey(status.Creator.Login)
		if key == "" {
			key = checkBotKey(status.Context)
		}
		if key == "" {
			key = "unknown"
		}
		b := get(key)
		b.hasCheck = true
		b.pending = b.pending || status.State == "pending" || status.State == ""
		b.failed = b.failed || (status.State != "pending" && status.State != "" && status.State != "success")
	}
	for _, c := range s.Comments {
		if !isBot(c.User) || checkBotKey(c.User.Login) == "" {
			continue
		}
		switch c.Kind {
		case "reviews", "comments":
			if s.Pull.Head.SHA == "" || c.Commit != s.Pull.Head.SHA {
				continue
			}
		case "issue-comments":
			// GitHub issue comments have no commit ID. Never use them as
			// findings or as evidence of a current-head approval.
			if c.Commit != "" && c.Commit != s.Pull.Head.SHA {
				continue
			}
		default:
			continue
		}
		b := get(checkBotKey(c.User.Login))
		if c.Kind == "comments" {
			b.Findings++
		}
		if c.Kind == "reviews" && newerBotComment(c, b.review) {
			b.review = c
		}
		if c.URL != "" && !strings.EqualFold(c.State, "PENDING") && newerBotComment(c, b.link) {
			b.link = c
		}
	}
	out := make([]state.BotReview, 0, len(bots))
	for _, b := range bots {
		switch {
		case b.pending || strings.EqualFold(b.review.State, "PENDING"):
			b.State = "pending"
		case b.failed:
			b.State = "changes_requested"
		case strings.EqualFold(b.review.State, "CHANGES_REQUESTED"):
			b.State = "changes_requested"
		case strings.EqualFold(b.review.State, "APPROVED"):
			b.State = "approved"
		case strings.EqualFold(b.review.State, "COMMENTED"):
			b.State = "commented"
		case b.Findings > 0:
			b.State = "commented"
		case b.hasCheck:
			// A successful check is not a pull request review. Do not show an
			// approval unless GitHub returned an approving review above.
			continue
		}
		b.URL = b.link.URL
		out = append(out, b.BotReview)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bot < out[j].Bot })
	return out
}

func botCheckIdentity(c Check) string {
	for _, name := range []string{c.App.Slug, c.App.Name} {
		if key := checkBotKey(name); key != "" {
			return key
		}
	}
	if c.App.ID != 0 {
		return fmt.Sprintf("app%d", c.App.ID)
	}
	if key := checkBotKey(c.Name); key != "" {
		return key
	}
	return "unknown"
}

func newerBotComment(c, old Comment) bool {
	at, previous := commentTime(c), commentTime(old)
	return at.After(previous) || (at.Equal(previous) && c.ID > old.ID)
}
