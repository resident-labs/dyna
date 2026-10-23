package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/subagent"
)

type ChangeNotes = state.AuthorNotes
type Finding = state.Finding

type OpenChangeOptions struct {
	PolicyOverride PolicyOverride
	Dir            string
	Base           string
	Title          string
	Notes          ChangeNotes
	Category       string
	Dependencies   []string
	// Goal links the change to this goal of its room, replacing any link.
	// Empty keeps the link, or captures one when the change is new.
	Goal string
	// Fixes lists the issues a merge closes, replacing the existing list.
	// Nil keeps the links that the body and earlier calls set.
	Fixes []state.IssueRef
}

// gitRead performs bounded, read-only repository inspection without shell expansion.
func gitRead(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := gitCommand(ctx, dir, args...)

	data, err := childenv.Clean(cmd).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(data)), nil
}
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return gitexec.Command(ctx, dir, args...)
}

func changeID(r Record) string {
	if r.ChangeID != "" {
		return r.ChangeID
	}
	return r.Key
}
func changeKind(r Record) string {
	if r.Kind == "local" {
		return "local"
	}
	return "github"
}
func approved(verdict state.ReviewVerdict) bool { return facts.ApprovedVerdict(verdict) }
func changeLocation(r Record) string {
	if r.URL != "" {
		return r.URL
	}
	return changeID(r)
}
func changeLabel(r Record) string {
	if changeKind(r) == "github" {
		return fmt.Sprintf("PR #%d", r.Number)
	}
	return "Change " + r.Key
}
func cleanNotes(notes ChangeNotes) ChangeNotes {
	return ChangeNotes{Intent: Sanitize(notes.Intent), Testing: Sanitize(notes.Testing), Risks: Sanitize(notes.Risks), Questions: Sanitize(notes.Questions)}
}
func validateNotes(notes ChangeNotes) error {
	data, _ := json.Marshal(notes)
	if len(data) > 12000 {
		return fmt.Errorf("author notes exceed 12000 bytes")
	}
	return nil
}
func validateCategory(value string) error {
	switch value {
	case "", "fix", "small", "refactor", "move":
		return nil
	}
	return fmt.Errorf("category must be fix, small, refactor, or move")
}
func localRepo(ctx context.Context, dir string) (path, repo string, err error) {
	path, err = gitRead(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return
	}
	path = filepath.Clean(path)
	remote, _ := gitRead(ctx, dir, "config", "--get", "remote.origin.url")
	repo = repoName(remote)
	if repo == "" {
		repo = path
	}
	return
}

// repoName uses the shared SSH/HTTPS repository normalization.
func repoName(remote string) string {
	if remote == "" {
		return ""
	}
	return repoid.RepoName(remote)
}

func (s *Service) OpenChange(ctx context.Context, actor authority.Actor, author subagent.Info, opts OpenChangeOptions) (Record, bool, error) {
	if err := authority.Check("git change open", actor, nil); err != nil {
		return Record{}, false, err
	}
	// An author with no ID has notices delivered to the owner.
	if author.RoomID == "" {
		return Record{}, false, fmt.Errorf("an author room is required")
	}
	opts.PolicyOverride.Reason = Sanitize(opts.PolicyOverride.Reason)
	if err := changepolicy.ValidateOverride(opts.PolicyOverride); err != nil {
		return Record{}, false, err
	}
	if err := validateNotes(opts.Notes); err != nil {
		return Record{}, false, err
	}
	if err := validateCategory(opts.Category); err != nil {
		return Record{}, false, err
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return Record{}, false, err
	}
	branch, err := gitRead(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Record{}, false, fmt.Errorf("open a change from a named branch: %w", err)
	}
	dirty, err := gitRead(ctx, dir, "status", "--porcelain")
	if err != nil {
		return Record{}, false, err
	}
	if dirty != "" {
		return Record{}, false, fmt.Errorf("commit or remove worktree changes before opening a change; review uses committed content")
	}
	head, err := gitRead(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Record{}, false, err
	}
	baseRef := opts.Base
	if baseRef == "" {
		baseRef = "origin/main"
		enabled, err := locallane.Enabled(ctx, dir)
		if err != nil {
			return Record{}, false, err
		}
		if enabled {
			if _, err := locallane.Ensure(ctx, dir); err != nil {
				return Record{}, false, err
			}
			baseRef = locallane.Integration
		}
	}
	base, err := gitRead(ctx, dir, "rev-parse", "--verify", "--end-of-options", baseRef+"^{commit}")
	if err != nil && opts.Base == "" {
		baseRef = "main"
		base, err = gitRead(ctx, dir, "rev-parse", "--verify", "main^{commit}")
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("base is unavailable; supply --base <ref>: %w", err)
	}
	path, repo, err := localRepo(ctx, dir)
	if err != nil {
		return Record{}, false, err
	}
	key := "change_" + digest(path + "\x00" + branch)[:20]
	if opts.Title == "" {
		opts.Title = branch
	}
	record := Record{PolicyOverride: opts.PolicyOverride, Key: key, ChangeID: key, Kind: "local", Repo: repo, RepoPath: path, Branch: branch, Base: base, BaseRef: baseRef, Title: Sanitize(opts.Title), Head: head, Author: author.ID, Parent: author.ParentSession, RoomID: author.RoomID, Dir: dir, LookupDir: dir, State: "open", Watching: true, Created: s.now(), Notes: cleanNotes(opts.Notes), Category: opts.Category, Dependencies: opts.Dependencies, Seen: map[string]string{}, Findings: []Finding{}, ReviewRounds: []state.ReviewRound{}}
	created := false
	err = s.Store.update(func(st *State) error {
		if err := sessionlife.Activate(filepath.Join(filepath.Dir(s.Store.Root), "sessions"), author.ID); err != nil {
			return err
		}
		if existing := recordByID(st, key); existing != nil {
			key = existing.Key
		}
		// If a PR already tracks this branch, author notes attach to that change.
		for _, old := range st.Records {
			if old.Branch == branch && (old.RepoPath == path || old.Repo == repo) {
				if old.RoomID != author.RoomID || old.Author != author.ID {
					return fmt.Errorf("this branch belongs to another room or author")
				}
				key = old.Key
				break
			}
		}
		if old := st.Records[key]; old != nil {
			if old.RoomID != author.RoomID || old.Author != author.ID {
				return fmt.Errorf("this branch belongs to another room or author")
			}
			if old.State != "open" {
				return fmt.Errorf("this change is closed; open a new branch")
			}
			if opts.Notes == (ChangeNotes{}) {
				record.Notes = old.Notes
			}
			if opts.Title == "" {
				record.Title = old.Title
			}
			if opts.Category == "" {
				record.Category = old.Category
			}
			if opts.Dependencies == nil {
				record.Dependencies = old.Dependencies
			}
			if old.Head != head || old.BaseRef != baseRef {
				resetReview(old, head)
				// TODO: legacy/shim remove later — a stored review from before the
				// typed results may still say question; remove when that history ages out.
			} else if old.Notes != record.Notes && (old.Verdict == "changes" || old.Verdict == "question") {
				old.Verdict = ""
				old.Reviewer = ""
				old.RoundHead = ""
			}
			old.Notes, old.Category, old.Dependencies = record.Notes, record.Category, record.Dependencies
			old.Base, old.BaseRef, old.RepoPath = base, baseRef, path
			old.Title = record.Title
			if !opts.PolicyOverride.Empty() {
				old.PolicyOverride = opts.PolicyOverride
			}
			old.Watching = true
			record = *old
			return nil
		}
		st.Records[key] = &record
		created = true
		return nil
	})
	return record, created, err
}
func resetReview(r *Record, head string) {
	headChanged := r.Head != head
	rememberReviewAssignment(r)
	r.Landing = nil
	retireReviewNotices(r, "head-changed:"+head)
	r.Head = head
	if headChanged && changeKind(*r) == "github" {
		r.CI = "pending"
	}
	r.CIKey = ""
	r.RoundHead = ""
	r.RoundStarted = time.Time{}
	r.Verdict = ""
	r.Summary = ""
	r.Reviewer = ""
	r.ReviewID = 0
	r.PauseReason = ""
}
func (s *Service) refreshLocal(ctx context.Context, key string) error {
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	head, err := gitRead(ctx, r.RepoPath, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+r.Branch+"^{commit}")
	return s.Store.updateRecords([]string{key}, func(st *State) error {
		live := st.Records[key]
		if live == nil || !live.Watching {
			return nil
		}
		live.Checked = s.now()
		if err != nil {
			live.Error = Sanitize(err.Error())
			live.PauseReason = "review paused: local branch is unavailable"
			notice(live, "", "Local change cannot be reviewed because its branch is unavailable.", "local-branch-unavailable")
			return nil
		}
		live.Error = ""
		if head != live.Head {
			resetReview(live, head)
		}
		return nil
	})
}

// sameAuthor refuses a write when the change's author is no longer the one
// its caller checked, such as after a transfer in between.
func sameAuthor(r *Record, author string) error {
	if r.Author != author {
		return fmt.Errorf("the change's author changed; try again")
	}
	return nil
}

func recordByID(st *State, id string) *Record { return st.RecordByID(id) }
func (s *Service) Changes(roomID string) ([]Record, error) {
	st, err := s.Store.Read()
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, r := range st.Records {
		if roomID == "" || r.RoomID == roomID {
			copy := *r
			copy.Kind = changeKind(copy)
			copy.ChangeID = changeID(copy)
			if copy.Findings == nil {
				copy.Findings = []Finding{}
			}
			if copy.ReviewRounds == nil {
				copy.ReviewRounds = []state.ReviewRound{}
			}
			out = append(out, copy)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// ReplyFinding records a reply to a finding. by is the replying session, or ""
// for the owner. Its caller checks who may reply against author, the change's
// author it saw.
func (s *Service) ReplyFinding(ctx context.Context, key, author, by, findingID, disposition, message string) error {
	switch disposition {
	case "fixed", "wont-fix", "question", "resolved":
	default:
		return fmt.Errorf("reply must be fixed, wont-fix, or question")
	}
	message = Sanitize(strings.TrimSpace(message))
	if message == "" || len(message) > 4000 {
		return fmt.Errorf("reply message must contain 1 to 4000 bytes")
	}
	err := s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if !r.Watching || r.State != "open" {
			return fmt.Errorf("change is not open and watched")
		}
		if err := sameAuthor(r, author); err != nil {
			return err
		}
		reply := state.FindingReply{Disposition: disposition, Message: message, Head: r.Head, Author: by, At: s.now()}
		found := false
		for i := range r.Findings {
			if r.Findings[i].ID == findingID {
				r.Findings[i].Replies = append(r.Findings[i].Replies, reply)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("finding %q does not exist", findingID)
		}
		for i := range r.ReviewRounds {
			for j := range r.ReviewRounds[i].Findings {
				if r.ReviewRounds[i].Findings[j].ID == findingID {
					r.ReviewRounds[i].Findings[j].Replies = append(r.ReviewRounds[i].Findings[j].Replies, reply)
				}
			}
		}
		// An author reply to requested changes or a question can ask for another
		// review on the same head. Head changes already reset the assignment.
		// TODO: legacy/shim remove later — a stored review from before the typed
		// results may still say question, which also reopens review; remove when
		// that history ages out.
		if disposition != "resolved" && (r.Verdict == "changes" || r.Verdict == "question") {
			r.Verdict = ""
			r.Reviewer = ""
			r.RoundHead = ""
			r.PauseReason = ""
		}
		return nil
	})
	return err
}

// CloseChange stops watching a change. Its caller checks who may close it
// against author, the change's author it saw.
func (s *Service) CloseChange(key, author string) error {
	err := s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if err := sameAuthor(r, author); err != nil {
			return err
		}
		endRecord(r, false, s.now())
		r.Watching = false
		r.PauseReason = "change closed"
		return nil
	})
	if err != nil {
		return err
	}
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	return s.cleanupEndedRoom(context.Background(), r.RoomID)
}

// UpdateChangeNotes replaces a change's author notes. Its caller checks who may
// change them against author, the change's author it saw.
func (s *Service) UpdateChangeNotes(key, author string, notes ChangeNotes, category string, deps []string) error {
	if err := validateNotes(notes); err != nil {
		return err
	}
	if err := validateCategory(category); err != nil {
		return err
	}
	return s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if err := sameAuthor(r, author); err != nil {
			return err
		}
		r.Notes, r.Category, r.Dependencies = cleanNotes(notes), category, append([]string{}, deps...)
		return nil
	})
}

// SetGoal links the change to a goal of its room; "" clears the link. The
// caller has checked the goal and the right to change the link against author.
func (s *Service) SetGoal(key, author, goalID string) error {
	return s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if err := sameAuthor(r, author); err != nil {
			return err
		}
		r.GoalID = goalID
		return nil
	})
}

func (s *Service) SetFixes(key, author string, fixes []state.IssueRef) (Record, error) {
	var out Record
	err := s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if err := sameAuthor(r, author); err != nil {
			return err
		}
		r.Fixes = state.NormalizeFixes(fixes)
		out = *r
		return nil
	})
	return out, err
}

// endRecord sets the terminal state and its stable pagination time together.
func endRecord(r *Record, merged bool, at time.Time) {
	if r.ClosedAt.IsZero() {
		r.ClosedAt = at
	}
	r.State = "closed"
	if merged {
		r.State = "merged"
	}
	if r.Watching {
		outcomeNotice(r)
	}
}
