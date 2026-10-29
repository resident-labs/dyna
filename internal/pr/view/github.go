package view

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/subagent"
)

// The PRs and issues view reads GitHub through the gh CLI in the room's
// repo, using a saved GitHub account that can read the repo's remote. Dyna
// stores no GitHub token. GitHub calls only read; TrackPR updates Dyna.

const ghTimeout = 20 * time.Second

type githubRepository struct {
	NameWithOwner string `json:"nameWithOwner"`
	URL           string `json:"url"`
}

func readGitHubRepository(ctx context.Context, root string) (githubRepository, error) {
	b, err := runGH(ctx, root, "repo", "view", "--json", "nameWithOwner,url")
	if err != nil {
		return githubRepository{}, err
	}
	var repo githubRepository
	err = json.Unmarshal(b, &repo)
	return repo, err
}

// GitHubListLimit is the most rows one list call returns.
const GitHubListLimit = 60

type GitHubUser struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
	IsBot bool   `json:"isBot,omitempty"`
}

type GitHubLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// GitHubItem is one pull request or issue row. Issue rows also carry work facts.
type GitHubItem struct {
	WorkingSession     string           `json:"workingSession,omitempty"`
	WorkingSessionName string           `json:"workingSessionName,omitempty"`
	WorkingSessionLive bool             `json:"workingSessionLive"`
	LinkedPRs          []GitHubLinkedPR `json:"linkedPRs,omitempty"`
	Group              string           `json:"group,omitempty"`
	Number             int              `json:"number"`
	Title              string           `json:"title"`
	State              string           `json:"state"`
	StateReason        string           `json:"stateReason,omitempty"`
	IsDraft            bool             `json:"isDraft,omitempty"`
	Author             GitHubUser       `json:"author"`
	Labels             []GitHubLabel    `json:"labels"`
	Assignees          []GitHubUser     `json:"assignees,omitempty"`
	CommentCount       int              `json:"commentCount,omitempty"`
	CreatedAt          time.Time        `json:"createdAt"`
	UpdatedAt          time.Time        `json:"updatedAt"`
	ClosedAt           *time.Time       `json:"closedAt,omitempty"`
	MergedAt           *time.Time       `json:"mergedAt,omitempty"`
	URL                string           `json:"url"`
	HeadRefName        string           `json:"headRefName,omitempty"`
	BaseRefName        string           `json:"baseRefName,omitempty"`
	ReviewDecision     string           `json:"reviewDecision,omitempty"`
	Additions          int              `json:"additions,omitempty"`
	Deletions          int              `json:"deletions,omitempty"`
}

type GitHubListResult struct {
	Account string       `json:"account,omitempty"`
	Repo    string       `json:"repo"`
	URL     string       `json:"url"`
	Kind    string       `json:"kind"`
	State   string       `json:"state"`
	Items   []GitHubItem `json:"items"`
	// More is true when the list hit GitHubListLimit.
	More bool `json:"more"`
}

type GitHubComment struct {
	Author    GitHubUser `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	URL       string     `json:"url,omitempty"`
}

type GitHubReview struct {
	Author      GitHubUser `json:"author"`
	Body        string     `json:"body"`
	State       string     `json:"state"`
	SubmittedAt time.Time  `json:"submittedAt"`
}

// GitHubCheck joins gh's two kinds of status rows: a CheckRun and a
// commit StatusContext. Result is one of pending, success, failure,
// skipped or neutral.
type GitHubCheck struct {
	Name     string `json:"name"`
	Workflow string `json:"workflow,omitempty"`
	Result   string `json:"result"`
	URL      string `json:"url,omitempty"`
}

type GitHubFile struct {
	Path       string `json:"path"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
	ChangeType string `json:"changeType"`
}

// GitHubCommit is one commit of a pull request, oldest first.
type GitHubCommit struct {
	SHA      string     `json:"sha"`
	Headline string     `json:"headline"`
	Author   GitHubUser `json:"author"`
	Date     time.Time  `json:"date"`
}

type GitHubDetail struct {
	GitHubItem
	Kind         string          `json:"kind"`
	Body         string          `json:"body"`
	Comments     []GitHubComment `json:"comments"`
	Reviews      []GitHubReview  `json:"reviews"`
	Checks       []GitHubCheck   `json:"checks"`
	Files        []GitHubFile    `json:"files"`
	Commits      []GitHubCommit  `json:"commits"`
	ChangedFiles int             `json:"changedFiles,omitempty"`
	Mergeable    string          `json:"mergeable,omitempty"`
}

// ghItem is gh's JSON for a row. gh names the bot flag is_bot.
type ghItem struct {
	GitHubItem
	ClosedByPullRequestsReferences []GitHubLinkedPR `json:"closedByPullRequestsReferences"`
	Author                         ghPerson         `json:"author"`
}

func (g ghItem) item() GitHubItem {
	it := g.GitHubItem
	it.LinkedPRs = g.ClosedByPullRequestsReferences
	it.Author = g.Author.user()
	if it.Labels == nil {
		it.Labels = []GitHubLabel{}
	}
	return it
}

const (
	ghItemFields = "number,title,state,author,labels,createdAt,updatedAt,closedAt,url"
	ghPRFields   = ghItemFields + ",isDraft,mergedAt,headRefName,baseRefName,reviewDecision,additions,deletions"
)

func ghKind(kind string) (string, error) {
	switch kind {
	case "pr", "issue":
		return kind, nil
	}
	return "", fmt.Errorf("kind must be pr or issue, not %q", kind)
}

// runGH runs gh in root. gh never prompts, pages or colors here.
func runGH(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := ghclient.Command(ctx, "", root, args...)
	stdout := &gitsvc.CappedBuffer{Limit: filesvc.MaxWorkspaceBytes}
	stderr := &gitsvc.CappedBuffer{Limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := ghclient.Run(ctx, cmd)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("the GitHub CLI (gh) is not installed on this computer. Install it, then run gh auth login")
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("gh timed out: %w", ctx.Err())
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New("gh: " + msg)
	}
	if stdout.Truncated {
		return stdout.Bytes(), errTruncated
	}
	return stdout.Bytes(), nil
}

var errTruncated = errors.New("gh output was too large")

// GitHubList lists a room repo's pull requests (kind "pr") or issues
// (kind "issue"), newest first. state is open, closed, merged (PRs only)
// or all. search is GitHub search syntax, such as "author:me label:bug".
func (s *Service) GitHubList(roomRef, kind, state, search string) (GitHubListResult, error) {
	kind, err := ghKind(kind)
	if err != nil {
		return GitHubListResult{}, err
	}
	switch state {
	case "":
		state = "open"
	case "open", "closed", "all":
	case "merged":
		if kind != "pr" {
			return GitHubListResult{}, errors.New("only pull requests can be merged")
		}
	default:
		return GitHubListResult{}, fmt.Errorf("state must be open, closed, merged or all, not %q", state)
	}
	root, err := s.WorkspaceRoot(roomRef, "")
	if err != nil {
		return GitHubListResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), roomRef)

	if kind == "issue" {
		return s.githubIssueList(ctx, root, roomRef, state, search)
	}

	repoc := make(chan githubRepository, 1)
	go func() {
		r, _ := readGitHubRepository(ctx, root)
		repoc <- r
	}()

	fields := ghPRFields
	args := []string{kind, "list", "--state", state, "--limit", strconv.Itoa(GitHubListLimit), "--json", fields}
	if search = strings.TrimSpace(search); search != "" {
		args = append(args, "--search", search)
	}
	b, err := runGH(ctx, root, args...)
	repo := <-repoc
	if err != nil {
		return GitHubListResult{}, err
	}
	var rows []ghItem
	if err := json.Unmarshal(b, &rows); err != nil {
		return GitHubListResult{}, fmt.Errorf("read gh %s list: %w", kind, err)
	}
	out := GitHubListResult{Account: ghclient.Account(ctx, "", root), Repo: repo.NameWithOwner, URL: repo.URL, Kind: kind, State: state, Items: make([]GitHubItem, 0, len(rows)), More: len(rows) >= GitHubListLimit}
	for _, r := range rows {
		out.Items = append(out.Items, r.item())
	}
	return out, nil
}

// GitHubView reads one pull request or issue with its body, comments and,
// for a PR, its reviews, checks and changed files.
func (s *Service) GitHubView(roomRef, kind string, number int) (GitHubDetail, error) {
	kind, err := ghKind(kind)
	if err != nil {
		return GitHubDetail{}, err
	}
	if number <= 0 {
		return GitHubDetail{}, errors.New("number must be positive")
	}
	root, err := s.WorkspaceRoot(roomRef, "")
	if err != nil {
		return GitHubDetail{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), roomRef)
	if kind == "issue" {
		return s.githubIssueView(ctx, root, roomRef, number)
	}
	fields := ghPRFields + ",body,comments,reviews,statusCheckRollup,files,commits,changedFiles,mergeable"
	b, err := runGH(ctx, root, kind, "view", strconv.Itoa(number), "--json", fields)
	if err != nil {
		return GitHubDetail{}, err
	}
	return parseGitHubDetail(kind, b)
}

type ghPerson struct {
	Login    string `json:"login"`
	Name     string `json:"name"`
	IsBot    bool   `json:"is_bot"`
	Typename string `json:"__typename"`
}

func (p ghPerson) user() GitHubUser {
	return GitHubUser{Login: p.Login, Name: p.Name, IsBot: p.IsBot || p.Typename == "Bot"}
}

func parseGitHubDetail(kind string, b []byte) (GitHubDetail, error) {
	var raw struct {
		ghItem
		Body     string `json:"body"`
		Comments []struct {
			Author    ghPerson  `json:"author"`
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"createdAt"`
			URL       string    `json:"url"`
		} `json:"comments"`
		Reviews []struct {
			Author      ghPerson  `json:"author"`
			Body        string    `json:"body"`
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"reviews"`
		Checks []struct {
			Typename   string `json:"__typename"`
			Name       string `json:"name"`
			Workflow   string `json:"workflowName"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			DetailsURL string `json:"detailsUrl"`
			Context    string `json:"context"`
			State      string `json:"state"`
			TargetURL  string `json:"targetUrl"`
		} `json:"statusCheckRollup"`
		Files   []GitHubFile `json:"files"`
		Commits []struct {
			OID      string     `json:"oid"`
			Headline string     `json:"messageHeadline"`
			Authors  []ghPerson `json:"authors"`
			Date     time.Time  `json:"authoredDate"`
		} `json:"commits"`
		ChangedFiles int    `json:"changedFiles"`
		Mergeable    string `json:"mergeable"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return GitHubDetail{}, fmt.Errorf("read gh %s view: %w", kind, err)
	}
	out := GitHubDetail{
		GitHubItem: raw.item(), Kind: kind, Body: raw.Body,
		Comments: []GitHubComment{}, Reviews: []GitHubReview{}, Checks: []GitHubCheck{}, Files: raw.Files,
		Commits:      []GitHubCommit{},
		ChangedFiles: raw.ChangedFiles, Mergeable: raw.Mergeable,
	}
	if out.Files == nil {
		out.Files = []GitHubFile{}
	}
	for _, c := range raw.Commits {
		var who GitHubUser
		if len(c.Authors) > 0 {
			who = c.Authors[0].user()
		}
		out.Commits = append(out.Commits, GitHubCommit{SHA: c.OID, Headline: c.Headline, Author: who, Date: c.Date})
	}
	for _, c := range raw.Comments {
		out.Comments = append(out.Comments, GitHubComment{Author: c.Author.user(), Body: c.Body, CreatedAt: c.CreatedAt, URL: c.URL})
	}
	for _, r := range raw.Reviews {
		out.Reviews = append(out.Reviews, GitHubReview{Author: r.Author.user(), Body: r.Body, State: r.State, SubmittedAt: r.SubmittedAt})
	}
	for _, c := range raw.Checks {
		if c.Typename == "StatusContext" {
			out.Checks = append(out.Checks, GitHubCheck{Name: c.Context, Result: checkResult("COMPLETED", c.State), URL: c.TargetURL})
			continue
		}
		out.Checks = append(out.Checks, GitHubCheck{Name: c.Name, Workflow: c.Workflow, Result: checkResult(c.Status, c.Conclusion), URL: c.DetailsURL})
	}
	return out, nil
}

// checkResult maps a CheckRun status and conclusion, or a StatusContext
// state, to one word the view colors.
func checkResult(status, conclusion string) string {
	if status != "" && status != "COMPLETED" {
		return "pending"
	}
	switch conclusion {
	case "SUCCESS":
		return "success"
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return "failure"
	case "SKIPPED":
		return "skipped"
	case "PENDING", "EXPECTED", "":
		return "pending"
	}
	return "neutral"
}

// GitHubDiff returns a pull request's unified diff, capped like GitDiff.
func (s *Service) GitHubDiff(roomRef string, number int) (gitsvc.GitDiffResult, error) {
	if number <= 0 {
		return gitsvc.GitDiffResult{}, errors.New("number must be positive")
	}
	root, err := s.WorkspaceRoot(roomRef, "")
	if err != nil {
		return gitsvc.GitDiffResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), roomRef)
	b, err := runGH(ctx, root, "pr", "diff", strconv.Itoa(number), "--color", "never")
	truncated := errors.Is(err, errTruncated)
	if err != nil && !truncated {
		return gitsvc.GitDiffResult{}, err
	}
	text := strings.ToValidUTF8(string(b), "�")
	return gitsvc.GitDiffResult{Text: text, Truncated: truncated}, nil
}

// TrackPR links a pull request in the room repo to a session in the room,
// as `dyna git pr track` does from inside that session, and starts the host
// watcher. A PR that was untracked starts watching again.
func (s *Service) TrackPR(ctx context.Context, roomRef, sessionRef string, number int) (ChangeView, error) {
	if number <= 0 {
		return ChangeView{}, errors.New("number must be positive")
	}
	r, created, err := s.track(ctx, roomRef, sessionRef, strconv.Itoa(number), "", "", nil)
	if err != nil {
		return ChangeView{}, err
	}
	p := s.Watcher()
	if !created && !r.Watching && r.State == prstate.PROpen {
		if err := p.Resume(r.Key); err != nil {
			return ChangeView{}, err
		}
		if r, err = p.Store.Get(r.Key); err != nil {
			return ChangeView{}, err
		}
	}
	st, err := p.Store.Read()
	if err != nil {
		return ChangeView{}, err
	}
	facts, err := p.ReviewFacts(st)
	if err != nil {
		return ChangeView{}, err
	}
	return changeView(facts, r), nil
}

// TrackedPR is a PR as `dyna git pr track` prints it.
type TrackedPR struct {
	PR      watch.Record `json:"pr"`
	Created bool         `json:"created"`
}

// TrackPRRef tracks a PR named by number or URL for the CLI. dir is the
// caller's checkout; the engine accepts it only as a checkout of the room's
// repository and of the PR's, and "" uses the room's workspace root. A goal
// ID links the PR to that goal, replacing any link. A non-nil fixes list
// replaces the issues that a merge closes.
func (s *Service) TrackPRRef(ctx context.Context, roomRef, sessionRef, ref, dir, goal string, fixes []prstate.IssueRef) (TrackedPR, error) {
	r, created, err := s.track(ctx, roomRef, sessionRef, ref, dir, goal, fixes)
	r.HeldComments = nil
	return TrackedPR{r, created}, err
}

// track is the one PR tracking path of the web app and the CLI. The caller
// acts in the PR's room, and only the owner or root names another author.
// The gh work runs before the state write, never under the PR lock.
func (s *Service) track(ctx context.Context, roomRef, sessionRef, refArg, dir, goal string, fixes []prstate.IssueRef) (watch.Record, bool, error) {
	rec, actor, info, err := s.authorIn(ctx, roomRef, sessionRef, "tracks a PR")
	if err != nil {
		return watch.Record{}, false, err
	}
	if err := s.checkGoalLink(rec.ID, goal); err != nil {
		return watch.Record{}, false, err
	}
	root, err := s.WorkspaceRoot(rec.ID, "")
	if err != nil {
		return watch.Record{}, false, err
	}
	explicit := dir != ""
	if !explicit {
		dir = root
	} else if err := CheckoutOfRoom(ctx, rec, dir); err != nil {
		return watch.Record{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), roomRef)
	p := s.Watcher()
	ref, err := p.Resolve(ctx, refArg, dir)
	if err != nil {
		return watch.Record{}, false, err
	}
	if explicit {
		root, err := room.RepoRoot(dir)
		if err != nil {
			return watch.Record{}, false, err
		}
		if err := checkoutOfPR(ctx, p.GH, ref, dir, rec.UpstreamOf(root)); err != nil {
			return watch.Record{}, false, err
		}
	}
	r, created, err := p.Track(ctx, actor, ref, info, dir)
	if err != nil {
		return watch.Record{}, false, err
	}
	if fixes != nil {
		if r, err = p.SetFixes(r.Key, r.Author, fixes); err != nil {
			return watch.Record{}, false, err
		}
	}
	if r.RoomID != rec.ID {
		return watch.Record{}, false, fmt.Errorf("PR #%d is tracked in another room", r.Number)
	}
	r.GoalID, err = s.linkGoal(ctx, p, r, info, created, goal)
	return r, created, err
}

// checkoutOfPR accepts dir when it is a checkout of the PR's base repository.
// A fork checkout is also accepted when upstream, the room's parent setting,
// is the base and the PR's head repository is the checkout's own repository,
// that is, the fork that opened the PR. The head is fetched only in that case.
func checkoutOfPR(ctx context.Context, gh watch.GitHub, ref watch.Ref, dir, upstream string) error {
	notCheckout := fmt.Errorf("%s is not a checkout of %s/%s", dir, ref.Host, ref.Repo)
	base, err := repoid.RemoteIdentity("", "https://"+ref.Host+"/"+ref.Repo)
	if err != nil {
		return notCheckout
	}
	checkout, err := repoid.Resolve(ctx, dir)
	if err != nil {
		return notCheckout
	}
	if checkout.ID == base {
		return nil
	}
	if upstream != base {
		return notCheckout
	}
	pull, err := gh.Pull(ctx, ref)
	if err != nil {
		return err
	}
	head, err := repoid.RemoteIdentity("", "https://"+ref.Host+"/"+pull.Head.Repo.FullName)
	if err != nil || checkout.ID != head {
		return notCheckout
	}
	return nil
}

// authorIn resolves the room and the author a caller names for new work. Only
// the owner or root names another session; any other caller acts as itself.
func (s *Service) authorIn(ctx context.Context, roomRef, sessionRef, doing string) (room.Record, authority.Actor, subagent.Info, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return room.Record{}, authority.Actor{}, subagent.Info{}, err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return room.Record{}, authority.Actor{}, subagent.Info{}, err
	}
	// A caller that names no session: root acts as its own session when its
	// stamp has one, and the owner and a sessionless root act as the room
	// owner. Any other sessionless caller is refused below.
	if sessionRef == "" {
		if actor.Role == authority.Root {
			sessionRef = actor.Session
		}
		if sessionRef == "" {
			// The authenticated owner has no session ID, so its work belongs to
			// the room owner and uses the room's workspace; so does root without
			// a session.
			if actor.Role == authority.Owner || actor.Role == authority.Root {
				return rec, actor, subagent.Info{Meta: subagent.Meta{RoomID: rec.ID, Room: rec.Name, Dir: rec.Repo}}, nil
			}
			// Any other sessionless caller, such as a workflow run coordinator
			// (the Worker role), cannot own new work. Refuse here, not at the
			// session store's "session id or address is required" (issue D-247).
			return room.Record{}, authority.Actor{}, subagent.Info{}, fmt.Errorf("this caller has no session, so it cannot %s; only sessions, root and the owner can", doing)
		}
	}
	info, err := s.SessionInRoom(rec.ID, sessionRef)
	if err != nil {
		return room.Record{}, authority.Actor{}, subagent.Info{}, err
	}
	if info.Status == subagent.StatusClosed {
		return room.Record{}, authority.Actor{}, subagent.Info{}, fmt.Errorf("%s is closed", info.Name)
	}
	if actor.Role != authority.Owner && actor.Role != authority.Root && info.ID != actor.Session {
		return room.Record{}, authority.Actor{}, subagent.Info{}, fmt.Errorf("this caller %s only as itself; ask root to do it for %s", doing, info.Name)
	}
	return rec, actor, info, nil
}

// OpenedChange is a change that OpenChange opened or updated.
type OpenedChange struct {
	Change  watch.Record `json:"change"`
	Created bool         `json:"created"`
}

// OpenChange opens a change from the caller's worktree dir, or, with prRef,
// links a GitHub PR and records its author notes. dir must be a checkout of
// the room's repository; a PR link without one uses the room's workspace.
func (s *Service) OpenChange(ctx context.Context, roomRef, sessionRef, prRef, dir string, opts watch.OpenChangeOptions) (OpenedChange, error) {
	rec, actor, info, err := s.authorIn(ctx, roomRef, sessionRef, "opens a change")
	if err != nil {
		return OpenedChange{}, err
	}
	if dir != "" {
		if err := CheckoutOfRoom(ctx, rec, dir); err != nil {
			return OpenedChange{}, err
		}
	}
	if err := s.checkGoalLink(rec.ID, opts.Goal); err != nil {
		return OpenedChange{}, err
	}
	p := s.Watcher()
	if prRef == "" {
		if opts.Fixes != nil {
			return OpenedChange{}, fmt.Errorf("--fixes links issues only on a GitHub PR; pass --pr")
		}
		if dir == "" {
			return OpenedChange{}, fmt.Errorf("open a change from a checkout of room %s's repository", rec.Name)
		}
		opts.Dir = dir
		r, created, err := p.OpenChange(ctx, actor, info, opts)
		if err == nil {
			r.GoalID, err = s.linkGoal(ctx, p, r, info, created, opts.Goal)
		}
		r.HeldComments = nil
		return OpenedChange{r, created}, err
	}
	if dir == "" {
		if dir, err = s.WorkspaceRoot(rec.ID, ""); err != nil {
			return OpenedChange{}, err
		}
	}
	ghCtx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	ghCtx = ghclient.WithRoom(ghclient.WithRooms(ghCtx, s.Notify.Rooms), rec.ID)
	ref, err := p.Resolve(ghCtx, prRef, dir)
	if err != nil {
		return OpenedChange{}, err
	}
	// Changing a tracked PR's notes or policy takes the author rule.
	checkedAuthor := info.ID
	if _, err := p.Store.Get(ref.Key()); err == nil {
		// A run-owned caller of the same controller first takes over the PR its
		// earlier run author left (issue D-252), before the author rule reads
		// the record; an ineligible caller keeps the refusal below.
		if _, err := p.TakeoverRunChange(ghCtx, ref.Key(), info, dir); err != nil {
			return OpenedChange{}, err
		}
		_, checked, err := s.authorPRInRoom(ctx, "UpdateChangeNotes", rec.ID, ref.Key())
		if err != nil {
			return OpenedChange{}, err
		}
		checkedAuthor = checked.Author
		if !opts.PolicyOverride.Empty() {
			if _, _, err := s.authorPRInRoom(ctx, "SetChangePolicyOverride", rec.ID, ref.Key()); err != nil {
				return OpenedChange{}, err
			}
		}
	}
	r, created, err := p.TrackWithPolicy(ghCtx, actor, ref, info, dir, opts.PolicyOverride, checkedAuthor)
	if err != nil {
		return OpenedChange{}, err
	}
	if opts.Fixes != nil {
		if r, err = p.SetFixes(r.Key, r.Author, opts.Fixes); err != nil {
			return OpenedChange{}, err
		}
	}
	if r.RoomID != rec.ID {
		return OpenedChange{}, fmt.Errorf("PR #%d is tracked in another room", r.Number)
	}
	if err := p.UpdateChangeNotes(r.Key, checkedAuthor, opts.Notes, opts.Category, opts.Dependencies); err != nil {
		return OpenedChange{}, err
	}
	if _, err := s.linkGoal(ctx, p, r, info, created, opts.Goal); err != nil {
		return OpenedChange{}, err
	}
	r, err = p.Store.Get(r.Key)
	r.HeldComments = nil
	return OpenedChange{r, created}, err
}

// CheckoutOfRoom refuses a caller's directory unless it is a checkout of the
// room's repository or of a repository registered on the room: a main
// checkout or one of its worktrees. A registered repository that left the disk
// is skipped, so it fails no check.
func CheckoutOfRoom(ctx context.Context, rec room.Record, dir string) error {
	if rec.Repo == "" && len(rec.ExtraRepos) == 0 {
		return fmt.Errorf("room %s has no repository, so it accepts no checkout path", rec.Name)
	}
	got, err := repoid.Resolve(ctx, dir)
	if err != nil {
		return fmt.Errorf("%s is not a checkout of room %s's repository: %w", dir, rec.Name, err)
	}
	// Each repository is checked on its own: one that is absent or unreadable
	// must not hide a match among the others.
	for _, path := range append([]string{rec.Repo}, rec.ExtraRepos...) {
		if path == "" {
			continue
		}
		if resolved, err := repoid.Resolve(ctx, path); err == nil && resolved.Main == got.Main {
			return nil
		}
	}
	return fmt.Errorf("%s is not a checkout of room %s's repository or of a repository registered on it", dir, rec.Name)
}
