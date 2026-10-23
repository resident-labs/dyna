package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/pr/state"
)

type ReviewContext struct {
	Policy         ChangePolicy       `json:"policy"`
	PolicyOverride PolicyOverride     `json:"policyOverride"`
	SkipReasons    []string           `json:"skipReasons"`
	GitHubChecks   []CheckSummary     `json:"githubChecks"`
	Comments       []Comment          `json:"comments"`
	Assignment     ReviewAssignment   `json:"assignment"`
	Previous       *state.ReviewRound `json:"previousReview"`
	Findings       []Finding          `json:"findings"`
	Rules          map[string]string  `json:"repoRules"`
	DiffPath       string             `json:"diffPath"`
	DiffStat       string             `json:"diffStat"`
	DeltaPath      string             `json:"deltaPath"`
	DiffTruncated  bool               `json:"diffTruncated"`
	DiffError      string             `json:"diffError"`
}

type boundedDiff struct {
	w         io.Writer
	left      int64
	truncated bool
}

func (w *boundedDiff) Write(p []byte) (int, error) {
	n := len(p)
	keep := int64(n)
	if keep > w.left {
		keep = w.left
		w.truncated = true
	}
	if keep > 0 {
		if _, err := w.w.Write(p[:keep]); err != nil {
			return 0, err
		}
		w.left -= keep
	}
	return n, nil
}
func writeDiff(ctx context.Context, repo, from, to, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer file.Close()
	cmd := gitCommand(ctx, repo, "diff", "--no-ext-diff", "--no-textconv", "--no-color", from+".."+to, "--")

	limit := &boundedDiff{w: file, left: 4 << 20}
	cmd.Stdout = limit
	if err = cmd.Run(); err != nil {
		return limit.truncated, fmt.Errorf("diff could not be prepared: %w", err)
	}
	return limit.truncated, file.Close()
}

// prepareReviewTask writes the assignment context. seen is the head this
// reviewer last saw without a verdict; the delta starts there, otherwise at
// the previous reviewed head.
func (s *Service) prepareReviewTask(ctx context.Context, r Record, seen string) (string, error) {
	var err error
	input := ReviewContext{GitHubChecks: r.Checks, Assignment: assignment(r), Findings: cloneFindings(r.Findings), Rules: map[string]string{}}
	if len(r.ReviewRounds) > 0 {
		prior := r.ReviewRounds[len(r.ReviewRounds)-1]
		input.Previous = &prior
	}
	dir := state.ReviewInputsDir(s.Store.Root, r.Key)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%d", digest(r.Head)[:16], r.Round)
	if r.RepoPath != "" && r.Base != "" {
		input.DiffPath = filepath.Join(dir, name+".patch")
		diffBase, e := gitRead(ctx, r.RepoPath, "merge-base", r.Base, r.Head)
		if e != nil {
			diffBase = r.Base
		}
		input.DiffTruncated, err = writeDiff(ctx, r.RepoPath, diffBase, r.Head, input.DiffPath)
		if err != nil {
			input.DiffError = Sanitize(err.Error())
			input.DiffPath = ""
		}
		input.DiffStat, _ = gitRead(ctx, r.RepoPath, "diff", "--no-ext-diff", "--no-textconv", "--stat", diffBase+".."+r.Head, "--")
		if from := deltaBase(input.Previous, seen); from != "" && from != r.Head {
			input.DeltaPath = filepath.Join(dir, name+"-delta.patch")
			truncated, e := writeDiff(ctx, r.RepoPath, from, r.Head, input.DeltaPath)
			input.DiffTruncated = input.DiffTruncated || truncated
			if e != nil {
				input.DeltaPath = ""
				input.DiffError = Sanitize(e.Error())
			}
		}
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "CONTRIBUTING.md"} {
		if r.RepoPath != "" {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cmd := gitCommand(readCtx, r.RepoPath, "show", r.Head+":"+name)
			var buffer bytes.Buffer
			limit := &boundedDiff{w: &buffer, left: 8192}
			cmd.Stdout = limit
			e := cmd.Run()
			cancel()
			if e == nil {
				text := buffer.String()
				if limit.truncated {
					text += "\n[Read the remaining rules with git show at the assigned head.]"
				}
				input.Rules[name] = Sanitize(text)
			}
		}
	}

	st, err := s.Store.Read()
	if err != nil {
		return "", err
	}
	input.Policy = EffectivePolicy(st, r)
	input.PolicyOverride = r.PolicyOverride
	input.SkipReasons = changepolicy.SkipReasons(input.Policy, r.PolicyOverride)
	input.Comments, err = s.reviewComments(ctx, st, r)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	task := reviewTask(r) + "\nRead the complete assignment context at " + path + ". It contains author notes, GitHub CI results, earlier findings and replies, repo rules, and diff paths."
	task += "\nRead source files at the assigned head with git show or a detached scratch worktree. The author's working directory can be on another branch. Do not treat its current files as the assigned head."
	task += "\nRead the full diff and diff stats. On a later round, inspect the delta since the previous reviewed head and confirm each earlier finding and author reply. Verify claims by reading code. Focus on correctness, behavior changes, missing tests, simpler alternatives, and duplicated logic. Do not add style findings handled by formatters."
	task += "\nRead comments in the assignment context: owner, human, bot and agent comments on this PR. Answer each comment in your final review. Confirm or reject every bot finding with a short reason after checking it against the assigned head, file, and line. Bot feedback does not establish passing CI. New comments and pushes arrive as steering while you work."
	task += "\nNever run the test suite. CI is the test oracle and runs in parallel with your review. You may run cheap static checks on changed files only, such as go vet, gofmt, tsc or a linter."
	task += "\nRecord changes as soon as you find a blocking problem, even before CI finishes. You may also approve before CI finishes. Dyna marks the change ready only when your approval and green CI are on the same head. CI failures go to the author, not to you."
	task += "\nNote the CI result you see for this head (passed, failed, pending or absent) in your final review. The assignment context lists the GitHub CI summaries."
	task += "\nReturn structured findings in a JSON array with id (optional stable ID), severity (blocking|should-fix|nit), file, line, why, fix, and optional patch. Use --findings-file when recording the verdict. Only you may sign it. Keep the review short."
	if from := deltaBase(input.Previous, seen); from != "" && from != r.Head {
		task += "\nYou last saw head " + from + ". The current head is " + r.Head + ". Review the delta from " + from + "; do not restart the review."
	}
	return task, nil
}

// deltaBase is the head an incremental review starts from.
func deltaBase(previous *state.ReviewRound, seen string) string {
	if seen != "" {
		return seen
	}
	if previous != nil {
		return previous.Head
	}
	return ""
}

// reviewComments lists every PR comment the reviewer must answer. It reuses
// the snapshot pages that supplied the watcher's triage and fetches when a
// page is missing. Known noise is dropped; the author's comments are kept,
// and the reviewer's own comments are left out.
func (s *Service) reviewComments(ctx context.Context, st State, r Record) ([]Comment, error) {
	out := []Comment{}
	if changeKind(r) != "github" {
		return out, nil
	}
	ref := refRecord(r)
	comments, complete := []Comment{}, true
	for _, kind := range []string{"reviews", "comments", "issue-comments"} {
		path := ref.path("/" + kind + "?per_page=100")
		if kind == "issue-comments" {
			path = fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", ref.Repo, ref.Number)
		}
		seen := map[string]bool{}
		for path != "" && complete {
			response, ok := st.Responses[r.Key][strings.ToLower(ref.Host)+"/"+path]
			var page []Comment
			if !ok || seen[path] || json.Unmarshal(response.Body, &page) != nil {
				complete = false
				break
			}
			seen[path] = true
			for _, c := range page {
				if kind == "reviews" && c.State == "PENDING" {
					continue
				}
				c.Kind = kind
				comments = append(comments, c)
			}
			path = response.Next
		}
	}
	if !complete {
		var err error
		comments, err = s.github(r.RoomID).Comments(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("read review comments: %w", err)
		}
	}
	pr := PRContext{URL: r.URL, AuthorSession: r.Author, FormerAuthors: r.FormerAuthors, OwnerLogin: r.OwnerLogin}
	rules := commentRules(st, ref)
	for _, c := range comments {
		owner := commentOwner(st.CommentSessions, c)
		if strings.TrimSpace(c.Body) == "" || (owner != "" && (owner == r.Reviewer || owner == r.LastReviewer)) {
			continue
		}
		event := Event{ID: commentKey(c), Kind: c.Kind, Commenter: c.User.Login, IsBot: isBot(c.User), Text: Sanitize(c.Body), ReviewState: c.State, Session: owner, Association: c.Association}
		decision := rules.Triage(pr, []Event{event})[0]
		if decision.Action == "hold" && r.AdmittedComments[commentKey(c)] == commentVersion(c) {
			decision = Decision{Action: "keep", Reason: "owner_admitted"}
		}

		if saved, ok := r.CommentDecisions[commentKey(c)]; ok && decision.Action == "keep" && saved.Action == "drop" && (r.Seen[commentKey(c)] == "" || r.Seen[commentKey(c)] == commentVersion(c)) {
			decision = saved
		}
		if reviewerHears(decision) {
			c.Body = clip(Sanitize(c.Body), 4000)
			if decision.Reason != "owner" {
				c.Body = quotedComment(c.Body)
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// reviewerHears keeps the author's comments, which the author's own feed drops.
func reviewerHears(d Decision) bool { return d.Action == "keep" || d.Reason == "author_marker" }
