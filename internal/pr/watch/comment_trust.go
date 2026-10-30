package watch

import (
	"context"
	"fmt"
	"strings"

	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/untrusted"
)

// commentRules fails closed: when settings cannot load, outside comments are held.
func commentRules(st State, ref Ref) Rules {
	rules := Rules{Learned: repoNoise(st, ref)}
	if cfg, err := settings.Load(""); err == nil {
		rules.TrustedBots = append([]string{}, cfg.PRTrustedBots...)
		rules.DeliverOutside = cfg.PROutsideComments == settings.OutsideCommentsDeliver
	}
	return rules
}

// externalComment wraps outside text in the same untrusted-data form that
// remote MCP results use.
func externalComment(body string) string {
	open, closing := untrusted.Tags("github-comment")
	return open + body + "\n" + closing
}

func commentNotice(e Event, d Decision, link string) string {
	body := clip(strings.TrimSpace(e.Text), 4000)
	if d.Reason != "owner" {
		body = externalComment(body + "\n" + link)
		link = ""
	}
	return fmt.Sprintf("GitHub %s from %s (comment %s):\n%s\n%s", e.Kind, e.Commenter, e.ID, body, link)
}

// AdmitComment admits only the held version. An edit requires a new decision.
func (s *Service) AdmitComment(key, id string, expectedVersion ...string) error {
	err := s.Store.updateRecords([]string{key}, func(st *State) error {
		r := st.Records[key]
		if r == nil {
			return fmt.Errorf("PR is not tracked")
		}
		held, ok := r.HeldComments[id]
		if !ok {
			return fmt.Errorf("comment %q is not held", id)
		}
		if len(expectedVersion) > 0 && held.Version != expectedVersion[0] {
			return fmt.Errorf("held comment changed; read its current version before admitting it")
		}
		if r.AdmittedComments == nil {
			r.AdmittedComments = map[string]string{}
		}
		r.AdmittedComments[id] = held.Version
		r.CommentDecisions[id] = Decision{Action: "keep", Reason: "owner_admitted"}
		delete(r.HeldComments, id)
		text := commentNotice(Event{ID: id, Kind: "comment", Commenter: held.Commenter, Text: held.Body}, r.CommentDecisions[id], held.URL)
		if r.Author != "" {
			r.Notices = append(r.Notices, state.Notice{ID: "admitted:" + id + ":" + held.Version, Target: r.Author, Text: text})
		}
		info, turn, err := s.reviewerInfo(r.Reviewer)
		if err == nil && needsReview(*st, *r) && prfacts.Busy(*r, info, turn) {
			systemReviewNotice(r, r.Reviewer, text, "admitted:"+id+":"+held.Version)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.flush(context.Background(), key)
}

func (s *Service) DismissComment(key, id string) error {
	return s.Store.updateRecords([]string{key}, func(st *State) error {
		r := st.Records[key]
		if r == nil {
			return fmt.Errorf("PR is not tracked")
		}
		if _, ok := r.HeldComments[id]; !ok {
			return fmt.Errorf("comment %q is not held", id)
		}
		delete(r.HeldComments, id)
		r.CommentDecisions[id] = Decision{Action: "hold", Reason: "owner_dismissed"}
		return nil
	})
}
