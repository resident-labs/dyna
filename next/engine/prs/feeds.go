package prs

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	// maxFeedPages is how far back one feed pages in a pass, about 1,000 items. Past it the log reports a gap.
	maxFeedPages = 34
	// feedOverlap is how far behind its newest item a feed may still show one for the first time.
	feedOverlap = 2 * time.Minute
	feedMain    = "main"
)

// listFeed is one of a repository's item feeds. Each lists newest first, 30 to a page.
type listFeed struct {
	name  string
	path  string
	parse func(data []byte, repo contract.RepoName, viewer string) ([]entry, error)
}

// listFeeds is read in this order: a state change shows on the issues feed first, so its event, read after, says who made it.
var listFeeds = []listFeed{
	{"issues", "/repos/%s/issues?state=all&sort=updated&direction=desc&per_page=30", parseIssues},
	{"comments", "/repos/%s/issues/comments?sort=updated&direction=desc&per_page=30", parseComments(contract.GitHubCommented)},
	{"review_comments", "/repos/%s/pulls/comments?sort=updated&direction=desc&per_page=30", parseComments(contract.GitHubReviewCommented)},
	{"events", "/repos/%s/issues/events?per_page=30", parseEvents},
}

// entry is one line of a feed. born is when it happened and listed is where the feed sorts it, which a comment's edit moves up.
// key is GitHub's ID for it. An entry with an empty item only says an item moved, or marks the feed's place.
type entry struct {
	key    string
	born   time.Time
	listed time.Time
	item   contract.GitHubItem
	seen   *FeedItem
}

// FeedItem is a PR or an issue as a feed lists it. Meta holds its labels, assignees, milestone and body hash.
type FeedItem struct {
	Number int
	Kind   contract.GitHubKind
	URL    string
	Title  string
	Author string
	Draft  bool
	State  string
	Meta   store.ItemMeta
}

type restItem struct {
	Number  int        `json:"number"`
	HTMLURL string     `json:"html_url"`
	Title   string     `json:"title"`
	Draft   bool       `json:"draft"`
	State   string     `json:"state"`
	Body    string     `json:"body"`
	User    *gqlAuthor `json:"user"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []gqlAuthor `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
	RepositoryURL string    `json:"repository_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// feedItem sorts the names, because GitHub's order means nothing.
func (i restItem) feedItem() FeedItem {
	item := FeedItem{Number: i.Number, Kind: contract.GitHubIssue, URL: i.HTMLURL, Title: i.Title, Author: login(i.User), Draft: i.Draft, State: i.State}
	if i.PullRequest != nil {
		item.Kind = contract.GitHubPR
	}
	hash := sha256.Sum256([]byte(i.Body))
	item.Meta.Body = hex.EncodeToString(hash[:8])
	for _, label := range i.Labels {
		item.Meta.Labels = append(item.Meta.Labels, label.Name)
	}
	for _, assignee := range i.Assignees {
		item.Meta.Assignees = append(item.Meta.Assignees, assignee.Login)
	}
	if i.Milestone != nil {
		item.Meta.Milestone = i.Milestone.Title
	}
	slices.Sort(item.Meta.Labels)
	slices.Sort(item.Meta.Assignees)
	return item
}

func parseIssues(data []byte, repo contract.RepoName, viewer string) ([]entry, error) {
	var items []restItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	entries := make([]entry, 0, 2*len(items))
	for _, raw := range items {
		seen := raw.feedItem()
		opened := contract.GitHubItem{Repo: repo, Number: raw.Number, Kind: seen.Kind, Action: contract.GitHubOpened, Actor: seen.Author, Author: seen.Author, URL: raw.HTMLURL, Detail: raw.Title, Text: shorten(raw.Body), Outside: seen.Author != viewer}
		entries = append(entries,
			entry{key: fmt.Sprintf("%d@%d", raw.Number, raw.UpdatedAt.Unix()), born: raw.UpdatedAt, listed: raw.UpdatedAt, seen: &seen},
			entry{key: fmt.Sprintf("#%d", raw.Number), born: raw.CreatedAt, listed: raw.UpdatedAt, item: opened})
	}
	return entries, nil
}

// parseComments reads a comment feed. An edit moves a comment up the feed and is no new comment, because its key stays and it was born long ago.
func parseComments(action contract.GitHubAction) func([]byte, contract.RepoName, string) ([]entry, error) {
	return func(data []byte, repo contract.RepoName, viewer string) ([]entry, error) {
		var comments []struct {
			ID        int64      `json:"id"`
			Body      string     `json:"body"`
			URL       string     `json:"html_url"`
			IssueURL  string     `json:"issue_url"`
			PullURL   string     `json:"pull_request_url"`
			User      *gqlAuthor `json:"user"`
			CreatedAt time.Time  `json:"created_at"`
			UpdatedAt time.Time  `json:"updated_at"`
		}
		if err := json.Unmarshal(data, &comments); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(comments))
		for _, comment := range comments {
			number, _ := strconv.Atoi(path.Base(comment.IssueURL + comment.PullURL))
			author := login(comment.User)
			item := contract.GitHubItem{Repo: repo, Number: number, Kind: contract.GitHubIssue, Action: action, Actor: author, URL: comment.URL, Text: shorten(comment.Body), Outside: author != viewer}
			if strings.Contains(comment.URL, "/pull/") {
				item.Kind = contract.GitHubPR
			}
			entries = append(entries, entry{key: strconv.FormatInt(comment.ID, 10), born: comment.CreatedAt, listed: comment.UpdatedAt, item: item})
		}
		return entries, nil
	}
}

// eventActions are the issue events the stream keeps. The rest, such as subscribed or referenced, say nothing a session or a
// subscription acts on, and the item's new updated_at reaches the issues feed anyway.
var eventActions = map[string]contract.GitHubAction{
	"closed": contract.GitHubClosed, "reopened": contract.GitHubReopened, "merged": contract.GitHubMerged,
	"labeled": contract.GitHubLabeled, "unlabeled": contract.GitHubUnlabeled,
	"assigned": contract.GitHubAssigned, "unassigned": contract.GitHubUnassigned,
	"milestoned": contract.GitHubMilestoned, "demilestoned": contract.GitHubDemilestoned, "renamed": contract.GitHubRenamed,
	"review_requested": contract.GitHubReviewRequested, "review_request_removed": contract.GitHubReviewRequestRemoved,
	"convert_to_draft": contract.GitHubConvertedToDraft, "ready_for_review": contract.GitHubReadyForReview,
	"head_ref_force_pushed": contract.GitHubForcePushed, "mentioned": contract.GitHubMentioned,
}

func parseEvents(data []byte, repo contract.RepoName, viewer string) ([]entry, error) {
	var events []struct {
		ID                int64      `json:"id"`
		Event             string     `json:"event"`
		Actor             *gqlAuthor `json:"actor"`
		Assigner          gqlAuthor  `json:"assigner"`
		Assignee          gqlAuthor  `json:"assignee"`
		ReviewRequester   gqlAuthor  `json:"review_requester"`
		RequestedReviewer gqlAuthor  `json:"requested_reviewer"`
		RequestedTeam     struct {
			Slug string `json:"slug"`
		} `json:"requested_team"`
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
		Milestone struct {
			Title string `json:"title"`
		} `json:"milestone"`
		Rename struct {
			To string `json:"to"`
		} `json:"rename"`
		Issue     restItem  `json:"issue"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, err
	}
	entries := make([]entry, 0, len(events))
	for _, event := range events {
		line := entry{key: strconv.FormatInt(event.ID, 10), born: event.CreatedAt, listed: event.CreatedAt}
		// A merge is also a close, and the merged event says it.
		merged := event.Issue.PullRequest != nil && event.Issue.PullRequest.MergedAt != nil
		if action, ok := eventActions[event.Event]; ok && !(action == contract.GitHubClosed && merged) {
			seen := event.Issue.feedItem()
			// On an assignment or a review request GitHub's actor can be the one assigned, and the one who did it has their own field.
			actor := cmp.Or(event.ReviewRequester.Login, event.Assigner.Login, login(event.Actor))
			detail := cmp.Or(event.Label.Name, event.Milestone.Title, event.Rename.To, event.Assignee.Login, event.RequestedReviewer.Login, event.RequestedTeam.Slug)
			line.item = contract.GitHubItem{Repo: repo, Number: event.Issue.Number, Kind: seen.Kind, Action: action, Actor: actor, Author: seen.Author, URL: seen.URL, Detail: detail, Outside: actor != viewer}
		}
		entries = append(entries, line)
	}
	return entries, nil
}

// feedRead is what walking one feed found. row is the feed's next stored state, to commit with what fresh leads to.
type feedRead struct {
	row   store.GitHubFeed
	fresh []entry
	moved bool
	gap   bool
}

// nextPage follows GitHub's Link header, because the issues feed pages by an opaque cursor as well as a number.
func nextPage(link string) string {
	for _, part := range strings.Split(link, ",") {
		target, rel, ok := strings.Cut(part, ";")
		if !ok || !strings.Contains(rel, `rel="next"`) {
			continue
		}
		if next, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>")); err == nil {
			return next.RequestURI()
		}
	}
	return ""
}

// noteInterval honours an X-Poll-Interval over the flat pass, which already keeps anything shorter.
func (c *Client) noteInterval(header http.Header, key string) {
	seconds, _ := strconv.Atoi(header.Get("X-Poll-Interval"))
	if wait := time.Duration(seconds) * time.Second; wait > PollEvery {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.resting == nil {
			c.resting = map[string]time.Time{}
		}
		c.resting[key] = c.Now().Add(wait)
	}
}

func (c *Client) restingFor(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Now().Before(c.resting[key])
}

// walk reads one feed down to its cursor. The first page carries the ETag, and a 304 there ends the walk at no cost. A feed
// with no stored row starts at its head: what it already lists is not news, so a deploy replays nothing.
func (c *Client) walk(ctx context.Context, repo contract.RepoName, feed listFeed, row store.GitHubFeed, viewer string) (feedRead, error) {
	started := row.Repo != ""
	read := feedRead{row: row}
	read.row.Repo, read.row.Feed = repo, feed.name
	cursor := &read.row.Cursor
	cursor.Seen = maps.Clone(row.Cursor.Seen)
	if cursor.Seen == nil {
		cursor.Seen = map[string]int64{}
	}
	from := row.Cursor.At.Add(-feedOverlap)
	if start := row.Cursor.Start.Add(time.Second); start.After(from) && !row.Cursor.Start.IsZero() {
		from = start
	}
	next, etag := fmt.Sprintf(feed.path, repo), row.ETag
	for page := 0; next != ""; page++ {
		if page == maxFeedPages {
			read.gap = true
			log.Printf("github %s %s: a gap: %d pages did not reach %s", repo, feed.name, maxFeedPages, row.Cursor.At.Format(time.RFC3339))
			break
		}
		header, data, err := c.get(ctx, next, etag)
		if err != nil {
			return read, err
		}
		if page == 0 {
			c.noteInterval(header, string(repo)+" "+feed.name)
		}
		if data == nil {
			return read, nil
		}
		if page == 0 {
			read.row.ETag, read.moved = header.Get("ETag"), true
		}
		entries, err := feed.parse(data, repo, viewer)
		if err != nil {
			return read, fmt.Errorf("GitHub %s feed: %w", feed.name, err)
		}
		for _, line := range entries {
			if line.born.After(cursor.At) {
				cursor.At = line.born
			}
			if !started || line.born.Before(from) {
				continue
			}
			if _, known := cursor.Seen[line.key]; !known {
				read.fresh = append(read.fresh, line)
				cursor.Seen[line.key] = line.born.Unix()
			}
		}
		if !started {
			cursor.Start = cursor.At
			break
		}
		if len(entries) == 0 || entries[len(entries)-1].listed.Before(from) {
			break
		}
		next, etag = nextPage(header.Get("Link")), ""
	}
	maps.DeleteFunc(cursor.Seen, func(_ string, born int64) bool { return born < cursor.At.Add(-feedOverlap).Unix() })
	return read, nil
}

// main reads the default branch's head. It learns the branch's name once, and again if the name stops answering.
func (c *Client) main(ctx context.Context, repo contract.RepoName, row store.GitHubFeed) (store.GitHubFeed, error) {
	row.Repo, row.Feed = repo, feedMain
	if row.Cursor.Branch == "" {
		_, data, err := c.get(ctx, "/repos/"+string(repo), "")
		if err != nil {
			return row, err
		}
		var about struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := json.Unmarshal(data, &about); err != nil {
			return row, fmt.Errorf("GitHub answer: %w", err)
		}
		row.Cursor.Branch = about.DefaultBranch
	}
	header, data, err := c.get(ctx, "/repos/"+string(repo)+"/git/ref/heads/"+row.Cursor.Branch, row.ETag)
	if errors.Is(err, ErrNotFound) {
		row.Cursor.Branch, row.ETag = "", ""
		return row, nil
	}
	if err != nil {
		return row, err
	}
	c.noteInterval(header, string(repo)+" "+feedMain)
	if data == nil {
		return row, nil
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		return row, fmt.Errorf("GitHub answer: %w", err)
	}
	row.ETag, row.Cursor.Head = header.Get("ETag"), ref.Object.SHA
	return row, nil
}

// Probe is what one repository's feeds showed in one pass.
type Probe struct {
	Items  []contract.GitHubItem // oldest first
	Seen   map[int]*FeedItem     // each item the issues feed listed anew, as it last stood
	Feeds  []store.GitHubFeed    // the rows that moved
	Main   string                // the default branch's head, when this pass read one
	Moved  bool                  // the default branch moved
	Resync bool                  // a feed lost its place or has none, so anything may have happened unseen
}

// Probe makes one conditional GET of each feed, and pages back only where one has news.
func (c *Client) Probe(ctx context.Context, repo contract.RepoName, rows map[string]store.GitHubFeed) (Probe, error) {
	probe := Probe{Seen: map[int]*FeedItem{}}
	var fresh []entry
	for _, feed := range listFeeds {
		if c.restingFor(string(repo) + " " + feed.name) {
			continue
		}
		read, err := c.walk(ctx, repo, feed, rows[feed.name], c.Viewer())
		if err != nil {
			return probe, err
		}
		probe.Resync = probe.Resync || read.gap || rows[feed.name].Repo == ""
		if read.moved {
			probe.Feeds = append(probe.Feeds, read.row)
		}
		fresh = append(fresh, read.fresh...)
	}
	if !c.restingFor(string(repo) + " " + feedMain) {
		was := rows[feedMain]
		row, err := c.main(ctx, repo, was)
		if err != nil {
			return probe, err
		}
		if row.ETag != was.ETag || row.Cursor.Branch != was.Cursor.Branch {
			probe.Feeds = append(probe.Feeds, row)
		}
		if row.Cursor.Head != was.Cursor.Head {
			probe.Main, probe.Moved = row.Cursor.Head, was.Cursor.Head != ""
		}
	}
	// A feed lists newest first, so reversing keeps its order within a second.
	slices.Reverse(fresh)
	slices.SortStableFunc(fresh, func(a, b entry) int { return a.born.Compare(b.born) })
	for _, line := range fresh {
		if line.seen != nil {
			probe.Seen[line.seen.Number] = line.seen
		}
	}
	for _, line := range fresh {
		if line.item.Action == "" {
			continue
		}
		if seen := probe.Seen[line.item.Number]; seen != nil {
			line.item.Author = seen.Author
		}
		probe.Items = append(probe.Items, line.item)
	}
	return probe, nil
}
