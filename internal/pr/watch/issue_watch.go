package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"dyna-agent/internal/pr/issuewatch"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

// issueObject is the part of an upstream GitHub issue the watcher reads.
type issueObject struct {
	Title   string `json:"title"`
	State   string `json:"state"`
	Updated string `json:"updated_at"`
}

// issueEvent is one entry of an issue's event log. Only closed and reopened
// entries become facts; the log's IDs only ever grow, so they make the cursor.
type issueEvent struct {
	ID    int64  `json:"id"`
	Event string `json:"event"`
}

// nextIssueEvents returns the facts new since the cursor w, and the cursor
// after them. A comment is new when its ID passes the cursor, so an edit to an
// older comment is not news. A close or reopen is new when its event ID passes
// the cursor, so a close and a reopen between two polls both reach the room.
func nextIssueEvents(w issuewatch.Watch, issue issueObject, comments []Comment, log []issueEvent) ([]room.IssueWatchEvent, issuewatch.Watch) {
	next := w
	next.Title = issue.Title
	slices.SortFunc(comments, func(a, b Comment) int { return cmp.Compare(a.ID, b.ID) })
	var events []room.IssueWatchEvent
	for _, c := range comments {
		next.CommentAt = max(next.CommentAt, c.Updated)
		if c.ID <= w.CommentID {
			continue
		}
		events = append(events, room.IssueWatchEvent{
			Type: room.IssueWatchComment, Repo: w.Repo, Number: w.Number, Title: issue.Title, URL: c.URL,
			Key: strconv.FormatInt(c.ID, 10), Commenter: c.User.Login,
			Summary: fmt.Sprintf("%s commented on %s#%d.", c.User.Login, w.Repo, w.Number),
			Body:    externalComment(c.Body),
		})
		next.CommentID = c.ID
	}
	slices.SortFunc(log, func(a, b issueEvent) int { return cmp.Compare(a.ID, b.ID) })
	for _, e := range log {
		if e.ID <= w.EventID {
			continue
		}
		next.EventID = e.ID
		var typ room.IssueWatchType
		var verb string
		switch e.Event {
		case "closed":
			typ, verb = room.IssueWatchClosed, "closed"
		case "reopened":
			typ, verb = room.IssueWatchReopened, "reopened"
		default:
			continue
		}
		events = append(events, room.IssueWatchEvent{
			Type: typ, Repo: w.Repo, Number: w.Number, Title: issue.Title, URL: issueURL(w),
			Key: strconv.FormatInt(e.ID, 10), Summary: fmt.Sprintf("%s#%d was %s.", w.Repo, w.Number, verb),
		})
	}
	next.State = issue.State
	next.Updated = issue.Updated
	return events, next
}

func issueURL(w issuewatch.Watch) string {
	return fmt.Sprintf("https://%s/%s/issues/%d", w.Host, w.Repo, w.Number)
}

func issuePath(ref Ref) string {
	return fmt.Sprintf("repos/%s/issues/%d", ref.Repo, ref.Number)
}

// eventsPrefix starts the request cache keys of one issue's events pages.
func eventsPrefix(ref Ref) string {
	return strings.ToLower(ref.Host) + "/" + issuePath(ref) + "/events"
}

// savedEvents loads the saved events pages as request cache entries, so the
// next poll sends their ETags and a GitHub 304 reuses each saved page.
func savedEvents(raw string) (map[string]cachedResponse, error) {
	pages := map[string]cachedResponse{}
	if raw == "" {
		return pages, nil
	}
	return pages, json.Unmarshal([]byte(raw), &pages)
}

// eventsJSON keeps the events pages a read used, for the next poll.
func eventsJSON(used map[string]cachedResponse, prefix string) (string, error) {
	pages := map[string]cachedResponse{}
	for key, page := range used {
		if strings.HasPrefix(key, prefix) {
			pages[key] = page
		}
	}
	b, err := json.Marshal(pages)
	return string(b), err
}

// issueComments reads the comments of an issue updated at or after since.
// An empty since reads every comment.
func issueComments(ctx context.Context, gh GitHub, ref Ref, since string) ([]Comment, error) {
	path := issuePath(ref) + "/comments?per_page=100"
	if since != "" {
		path += "&since=" + url.QueryEscape(since)
	}
	return readPages[Comment](ctx, gh, ref, path)
}

// issueEvents reads the whole event log of an issue. GitHub has no since filter
// for it, so the cursor filters the log.
func issueEvents(ctx context.Context, gh GitHub, ref Ref) ([]issueEvent, error) {
	return readPages[issueEvent](ctx, gh, ref, issuePath(ref)+"/events?per_page=100")
}

func readPages[T any](ctx context.Context, gh GitHub, ref Ref, path string) ([]T, error) {
	var pages [][]T
	if err := gh.pages(ctx, ref, path, &pages); err != nil {
		return nil, err
	}
	var out []T
	for _, page := range pages {
		out = append(out, page...)
	}
	return out, nil
}

// issueRepoAllowed reports whether a room may read an issue of host/repo. The
// room's own repository and its upstreams are allowed, and nothing else.
func issueRepoAllowed(ctx context.Context, rec room.Record, host, repo string) bool {
	identity := host + "/" + repo
	for _, up := range rec.Upstreams() {
		if strings.EqualFold(up, identity) {
			return true
		}
	}
	if rec.Repo == "" {
		return false
	}
	remote, err := repoid.Git(ctx, rec.Repo, "remote", "get-url", "origin")
	if err != nil {
		return false
	}
	own, err := repoid.RemoteIdentity(rec.Repo, remote)
	return err == nil && strings.EqualFold(own, identity)
}

// ResolveIssue names an upstream issue from its URL, or from a number in the
// repository of dir.
func (s *Service) ResolveIssue(ctx context.Context, text, dir string) (Ref, error) {
	if strings.Contains(text, "://") {
		u, err := url.Parse(text)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return Ref{}, fmt.Errorf("use an issue URL or number")
		}
		p := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(p) != 4 || p[2] != "issues" {
			return Ref{}, fmt.Errorf("expected an issue URL")
		}
		n, err := strconv.Atoi(p[3])
		if err != nil || n < 1 {
			return Ref{}, fmt.Errorf("invalid issue number")
		}
		return Ref{u.Host, p[0] + "/" + p[1], n}, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return Ref{}, fmt.Errorf("use an issue number or URL")
	}
	remote, err := repoid.Git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return Ref{}, err
	}
	identity, err := repoid.RemoteIdentity(dir, remote)
	if err != nil {
		return Ref{}, err
	}
	host, repo, ok := strings.Cut(identity, "/")
	if !ok {
		return Ref{}, fmt.Errorf("the repository has no host and owner")
	}
	return Ref{host, repo, n}, nil
}

// WatchIssue starts reading an upstream issue for the room rec. The issue's
// state at this call is the baseline, so only later facts reach the room.
func (s *Service) WatchIssue(ctx context.Context, rec room.Record, ref Ref) error {
	if !issueRepoAllowed(ctx, rec, ref.Host, ref.Repo) {
		return fmt.Errorf("%s is not this room's repository or one of its upstreams", ref.Repo)
	}
	gh := s.github(rec.ID)
	cache := &requestCache{used: map[string]cachedResponse{}, now: s.now}
	gh.requests = cache
	var issue issueObject
	if err := gh.api(ctx, ref, issuePath(ref), &issue); err != nil {
		return err
	}
	comments, err := issueComments(ctx, gh, ref, "")
	if err != nil {
		return err
	}
	log, err := issueEvents(ctx, gh, ref)
	if err != nil {
		return err
	}
	w := issuewatch.Watch{Room: rec.ID, Host: ref.Host, Repo: ref.Repo, Number: ref.Number,
		Title: issue.Title, State: issue.State, Updated: issue.Updated, CommentAt: issue.Updated}
	for _, c := range comments {
		w.CommentAt = max(w.CommentAt, c.Updated)
		w.CommentID = max(w.CommentID, c.ID)
	}
	for _, e := range log {
		w.EventID = max(w.EventID, e.ID)
	}
	saved := cache.used[strings.ToLower(ref.Host)+"/"+issuePath(ref)]
	w.ETag, w.Body = saved.ETag, string(saved.Body)
	if w.Events, err = eventsJSON(cache.used, eventsPrefix(ref)); err != nil {
		return err
	}
	return s.issueStore().Save(ctx, w)
}

// UnwatchIssue stops one room's read of an upstream issue. It reports whether
// the room watched the issue.
func (s *Service) UnwatchIssue(ctx context.Context, roomID string, ref Ref) (bool, error) {
	return s.issueStore().Remove(ctx, roomID, ref.Host, ref.Repo, ref.Number)
}

// pollIssueWatches reads every watched issue once. A changed issue is read
// again for its comments, and each new fact posts to its room root. It reports
// whether any issue is watched, so the tick stays due when no PR is watched.
func (s *Service) pollIssueWatches(ctx context.Context) (bool, error) {
	watches, err := s.issueStore().List(ctx)
	if err != nil {
		return false, err
	}
	var first error
	for _, w := range watches {
		if err := ctx.Err(); err != nil {
			return true, errors.Join(first, err)
		}
		first = errors.Join(first, s.pollIssueWatch(ctx, w))
	}
	return len(watches) > 0, first
}

func (s *Service) pollIssueWatch(ctx context.Context, w issuewatch.Watch) error {
	rec, err := s.roomsStore().Resolve(w.Room)
	if err != nil {
		return err
	}
	ref := Ref{Host: w.Host, Repo: w.Repo, Number: w.Number}
	key := strings.ToLower(w.Host) + "/" + issuePath(ref)
	entries, err := savedEvents(w.Events)
	if err != nil {
		return err
	}
	entries[key] = cachedResponse{ETag: w.ETag, Body: json.RawMessage(w.Body)}
	cache := &requestCache{entries: entries, used: map[string]cachedResponse{}, now: s.now}
	gh := s.github(rec.ID)
	gh.requests = cache
	var issue issueObject
	if err := gh.api(ctx, ref, issuePath(ref), &issue); err != nil {
		return err
	}
	var comments []Comment
	var log []issueEvent
	// A 304 means the issue representation matches its ETag, and a new comment
	// changes that representation. The event log is read on every poll: a close
	// and a reopen in one second can restore the same representation.
	if !cache.notModified[key] {
		if comments, err = issueComments(ctx, gh, ref, w.CommentAt); err != nil {
			return err
		}
	}
	if log, err = issueEvents(ctx, gh, ref); err != nil {
		return err
	}
	events, next := nextIssueEvents(w, issue, comments, log)
	for _, ev := range events {
		if _, err := s.roomsStore().PostEventInput(room.PostEventInput{
			RoomID: rec.ID, Event: ev, To: []string{rec.Name}, NeedsRoot: true, RoomService: true,
		}); err != nil {
			return err
		}
	}
	saved := cache.used[key]
	next.ETag, next.Body = saved.ETag, string(saved.Body)
	if next.Events, err = eventsJSON(cache.used, eventsPrefix(ref)); err != nil {
		return err
	}
	// A stale token means the watch was removed or re-watched during this poll.
	// Its cursor then stays as the newer save left it.
	_, err = s.issueStore().Advance(ctx, next)
	return err
}

func (s *Service) issueStore() issuewatch.Store {
	return issuewatch.Store{DataDir: s.dataDir()}
}

// dataDir is the engine data directory that holds dyna.db.
func (s *Service) dataDir() string {
	root := ""
	if s.Store != nil {
		root = filepath.Dir(s.Store.Root)
	}
	if root == "" || root == "." {
		root = store.DataDir()
	}
	return root
}
