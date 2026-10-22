package voice

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

// Tools are what Voice can do in its room: two read-only status answers,
// Dispatch, and answering the owner's open questions. Each status tool reads
// through the code its CLI command uses and answers in one to three short
// spoken sentences, with no tables, hashes or paths.
type Tools struct {
	link *RoomLink
	prs  *watch.Service
	now  func() time.Time

	mu   sync.Mutex
	sent map[string]time.Time // requests this call dispatched, by requestKey, and when
}

// NewTools gives Voice the tools for the room of link.
func NewTools(link *RoomLink) *Tools {
	prs := watch.New()
	prs.Notify = link.svc
	return &Tools{link: link, prs: prs, now: time.Now, sent: map[string]time.Time{}}
}

// PRStatus says the state, CI result, review verdict and merge queue place
// of the room's PR with that number, as `dyna git pr show` and
// `dyna git pr merge status` record them.
func (t *Tools) PRStatus(number int) (string, error) {
	st, err := t.prs.Store.Read()
	if err != nil {
		return "", err
	}
	// The room's latest PR with that number: two repos can share a number.
	key := ""
	var created time.Time
	for k, r := range st.Records {
		if r.RoomID == t.link.ID() && r.Number == number && (key == "" || r.Created.After(created)) {
			key, created = k, r.Created
		}
	}
	if key == "" {
		return fmt.Sprintf("I don't see PR %d in this room.", number), nil
	}
	pr, err := t.prs.Show(key)
	if err != nil {
		return "", err
	}
	queue, err := t.prs.MergeControlStatus(t.link.ID())
	if err != nil {
		return "", err
	}
	return prStatusText(pr.Record, queue.Queue), nil
}

func prStatusText(r watch.Record, queue []watch.MergeQueueItem) string {
	name := prName(r.Number, r.Title)
	switch r.State {
	case "merged":
		return spoken(name + " has merged.")
	case "closed":
		return spoken(name + " was closed without merging.")
	}
	draft := ""
	if r.IsDraft {
		draft = " as a draft"
	}
	ci := map[string]string{"passed": "CI passed", "failed": "CI failed", "pending": "CI is still running"}[r.CI]
	if ci == "" {
		ci = "CI has not reported yet"
	}
	review := "it has no review yet"
	if r.RoundHead == r.Head && r.Verdict != "" {
		review = map[string]string{
			"approve":           "the review approved it",
			"approve-with-nits": "the review approved it with small notes",
			"changes":           "the review asked for changes",
			"question":          "the reviewer has a question",
		}[r.Verdict]
	} else if r.Reviewer != "" {
		review = "a review is under way"
	}
	out := fmt.Sprintf("%s is open%s. %s, and %s.", name, draft, ci, review)
	for i, item := range queue {
		if item.URL == r.URL {
			out += fmt.Sprintf(" It is %s in the merge queue, %s.", ordinal(i+1), queuePhrase(item))
			break
		}
	}
	return spoken(out)
}

// MergeQueue says how many of the room's PRs are waiting or landing, and
// which one is next, from `dyna git pr merge status`.
func (t *Tools) MergeQueue() (string, error) {
	status, err := t.prs.MergeControlStatus(t.link.ID())
	if err != nil {
		return "", err
	}
	return mergeQueueText(status), nil
}

func mergeQueueText(status watch.MergeControlStatus) string {
	out := ""
	if status.Paused {
		out = "Merging is paused in this room. "
	}
	if len(status.Queue) == 0 {
		return spoken(out + "The merge queue is empty.")
	}
	landing := 0
	for _, item := range status.Queue {
		if landingNow(item) {
			landing++
		}
	}
	next := status.Queue[0]
	out += fmt.Sprintf("%s in the merge queue: %d landing and %d waiting. Next is %s, %s.",
		count(len(status.Queue), "PR is", "PRs are"), landing, len(status.Queue)-landing, itemName(next), queuePhrase(next))
	return spoken(out)
}

// rootAgentPost reports whether msg is a message or turn report between
// root and an agent. Root's address is the room's own, which it shares
// with the owner: a post from it that no person typed is root's, and an
// agent's post to it goes to root.
func (r *RoomLink) rootAgentPost(msg room.Message) bool {
	switch msg.Kind {
	case "", "message", "completed", "failed", "stopped":
	default:
		return false // service notices: PRs, issues, goals and instructions
	}
	toRoot := slices.ContainsFunc(msg.To, func(a string) bool { return a == r.rec.Name || a == r.rec.Name+":"+room.RootAgent })
	toAgent := slices.ContainsFunc(msg.To, r.agent)
	return r.fromRoot(msg) && toAgent || r.agent(msg.From) && toRoot
}

// agent reports whether addr names one of the room's agents: not root, not
// Voice and not the owner.
func (r *RoomLink) agent(addr string) bool {
	name, ok := strings.CutPrefix(addr, r.rec.Name+":")
	return ok && name != "" && name != room.RootAgent && name != room.VoiceAgent
}

// Dispatch's receipts. accepted means the agents have the request, and
// duplicate that the same request is still under way; neither means it is
// done. Its answer is spoken in the call when it comes, so Voice must not
// guess it.
const (
	workStarted   = "accepted. The request is under way; its answer comes later in this call. Say one short line, such as \"I've started that\", then stop and wait quietly. Say nothing about the result."
	workDuplicate = "duplicate. The same request is already under way, so it was not sent again. Say one short line, such as \"That's already under way\", then stop and wait quietly."
)

// Dispatch hands the owner's request to Dyna's agents: a post from Voice
// to root, like Ask. It returns at once. A request this call already sent,
// with no answer to the call since, is a duplicate and is not sent again.
func (t *Tools) Dispatch(ctx context.Context, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("the request is empty")
	}
	key := requestKey(text)
	t.mu.Lock()
	sent, again := t.sent[key]
	t.mu.Unlock()
	if again && !t.link.answeredSince(sent) {
		return workDuplicate, nil
	}
	if _, err := t.link.Ask(ctx, text); err != nil {
		return "", err
	}
	t.mu.Lock()
	t.sent[key] = t.now()
	t.mu.Unlock()
	return workStarted, nil
}

// requestKey is a request's words without case, spacing or end punctuation,
// so a request said again the same way is the same request.
func requestKey(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimRight(text, " .!?"))), " ")
}

// answerScan is how many of the room's last posts answeredSince reads.
const answerScan = 200

// answeredSince reports whether a post to the call has come since t: the
// answer to what the call sent then.
func (r *RoomLink) answeredSince(t time.Time) bool {
	scan := answerScan
	posts, _, err := r.svc.Rooms.Log(r.rec, room.LogOptions{All: true, Tail: &scan})
	if err != nil {
		return false
	}
	voice := r.rec.Name + ":" + room.VoiceAgent
	for _, msg := range posts {
		if msg.Kind != "voice" && slices.Contains(msg.To, voice) && !time.UnixMilli(msg.TS).Before(t) {
			return true
		}
	}
	return false
}

// questionAnswered is what Voice hears once the owner's answer is in.
const questionAnswered = "answered. The owner's answer is sent. Say one short line that you have it, then stop."

// AnswerQuestion answers an open question to the owner with the owner's
// words: the question id, or the only open one when id is empty. With
// several open, an empty id is refused, so an answer never lands on the
// wrong question. Words that name one of the question's options choose it.
func (t *Tools) AnswerQuestion(ctx context.Context, id, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("the answer is empty")
	}
	qs, err := t.link.svc.QuestionStore().List(t.link.ID())
	if err != nil {
		return "", err
	}
	var q *questions.Question
	for i := range qs {
		if qs[i].State != "open" || qs[i].To != "owner" || id != "" && qs[i].ID != id {
			continue
		}
		if q != nil {
			return "", errors.New("several questions are open; pass the question_id of the one the owner answered")
		}
		q = &qs[i]
	}
	if q == nil {
		return "", errors.New("there is no open question for the owner")
	}
	choice := ""
	for _, o := range q.Options {
		if strings.EqualFold(requestKey(o), requestKey(text)) {
			choice, text = o, ""
		}
	}
	owner := notify.Sender{Kind: notify.SenderVoice, Room: t.link.Name(), Human: true}
	if err := t.link.svc.AnswerQuestion(ctx, owner, "", q.ID, choice, text); err != nil {
		return "", err
	}
	return questionAnswered, nil
}

func prName(number int, title string) string {
	name := "PR " + strconv.Itoa(number)
	if title = phrase(title, 12); title != "" {
		name += ", " + title + ","
	}
	return name
}

// itemName names a merge queue entry: a PR by number and title, or a local change.
func itemName(item watch.MergeQueueItem) string {
	_, number, ok := strings.Cut(item.ChangeID, "#")
	if n, err := strconv.Atoi(number); ok && err == nil {
		return strings.TrimSuffix(prName(n, item.Title), ",")
	}
	return "a local change"
}

// landingNow reports whether the PR is in a merge trial that is still
// moving. A rejected, failed or unavailable trial is not landing; its reason
// is what Voice says.
func landingNow(item watch.MergeQueueItem) bool {
	switch item.State {
	case "trial-building", "trial-reviewing", "trial-running", "trial-stacking", "trial-publishing", "trial-published":
		return true
	}
	return false
}

func queuePhrase(item watch.MergeQueueItem) string {
	switch {
	case landingNow(item):
		return "landing now"
	case item.State == "ready":
		return "ready to land"
	case item.Reason != "":
		return phrase(item.Reason, 10)
	}
	return strings.ReplaceAll(item.State, "-", " ")
}

func ordinal(n int) string {
	if n <= 3 {
		return []string{"", "first", "second", "third"}[n]
	}
	return "number " + strconv.Itoa(n)
}

func count(n int, one, many string) string {
	if n == 1 {
		return "One " + one
	}
	return strconv.Itoa(n) + " " + many
}

// list joins up to max items, then says how many more there are.
func list(items []string, max int, sep string) string {
	if len(items) > max {
		return strings.Join(items[:max], sep) + fmt.Sprintf("%sand %d more", sep, len(items)-max)
	}
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], sep) + sep + "and " + items[len(items)-1]
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "Just now"
	case d < time.Hour:
		return count(int(d/time.Minute), "minute ago", "minutes ago")
	case d < 24*time.Hour:
		return count(int(d/time.Hour), "hour ago", "hours ago")
	}
	return count(int(d/(24*time.Hour)), "day ago", "days ago")
}

// phrase is the first sentence of text in at most max words, without
// markdown. It ends before the first path or hash, and the small words that
// led up to it.
func phrase(text string, max int) string {
	text = strings.NewReplacer("`", "", "*", "", "#", "", "_", " ").Replace(text)
	if i := strings.IndexAny(text, "\n"); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i]
	}
	words := strings.Fields(text)
	for i, w := range words {
		if unspeakable(w) {
			words = words[:i]
			break
		}
	}
	if len(words) > max {
		words = words[:max]
	}
	for len(words) > 0 && slices.Contains(leadIns, strings.ToLower(words[len(words)-1])) {
		words = words[:len(words)-1]
	}
	return strings.TrimRight(strings.Join(words, " "), ".:;,")
}

// leadIns are words that cannot end a phrase cut before a path or hash.
var leadIns = []string{"a", "an", "and", "at", "for", "from", "in", "into", "of", "on", "the", "to", "with"}

// hexRun finds hex runs as long as a short commit hash.
var hexRun = regexp.MustCompile(`[0-9a-fA-F]{7,}`)

// spoken drops words that make no sense aloud: paths, URLs and hashes or IDs.
func spoken(text string) string {
	words := strings.Fields(text)
	kept := words[:0]
	for _, w := range words {
		if unspeakable(w) {
			// Keep the end of a sentence that the dropped word closed.
			if n := len(kept); n > 0 && strings.HasSuffix(w, ".") && !strings.HasSuffix(kept[n-1], ".") {
				kept[n-1] += "."
			}
			continue
		}
		kept = append(kept, strings.TrimPrefix(w, "@"))
	}
	return strings.Join(kept, " ")
}

// unspeakable reports whether w is a path, a URL, or a hash or ID.
func unspeakable(w string) bool {
	w = strings.Trim(w, "\"'()[],.;:!?")
	return strings.Contains(w, "://") || strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~/") ||
		strings.HasPrefix(w, "./") || strings.Count(w, "/") >= 2 || hashLike(w)
}

// hashLike reports whether w holds a hex run with both digits and letters,
// as a commit hash or a post or session ID has.
func hashLike(w string) bool {
	for _, run := range hexRun.FindAllString(w, -1) {
		if strings.ContainsAny(run, "0123456789") && strings.ContainsAny(strings.ToLower(run), "abcdef") {
			return true
		}
	}
	return false
}
