// Package watch links GitHub pull requests to Dyna sessions and batches
// CI, review and owner feedback into durable room posts.
package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

const (
	MaxReviewBytes   = 12000
	PublishingLocal  = "local"
	PublishingGitHub = "github"
)

// PublishingMode fails closed for missing or legacy publishing settings.
func PublishingMode(value string) string {
	if value == PublishingGitHub {
		return PublishingGitHub
	}
	return PublishingLocal
}

func validatePublishing(value string) error {
	if value != "" && value != PublishingLocal && value != PublishingGitHub {
		return fmt.Errorf("review publishing must be local or github")
	}
	return nil
}

type Reviewer = state.Reviewer
type Notice = state.Notice
type Record = state.Record
type State = state.State
type RepositoryFacts = state.RepositoryFacts
type Store struct{ Root string }

func DefaultStore() *Store   { return &Store{Root: state.Root(store.DataDir())} }
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// errNoStoreChange aborts preparation before the shared writer publishes a no-op.
var errNoStoreChange = errors.New("no PR store change")

func (s *Store) update(fn func(*State) error) error {
	return storeUpdateResult(updateStore(context.Background(), s.Root, finishStoreUpdate(fn)))
}

// updateRecords gives a hot callback only its declared, isolated rows and
// metadata. The shared state writer merges them under the same PR fence.
func (s *Store) updateRecords(keys []string, fn func(*State) error) error {
	return storeUpdateResult(state.UpdateRecords(context.Background(), s.Root, keys, finishStoreUpdate(fn)))
}

// updateRecordOrAll decides the callback's scope under the PR lock, before it
// changes anything. Rare cross-record work keeps the generic isolated input.
func (s *Store) updateRecordOrAll(key string, needsAll func(*State) bool, fn func(*State) error) error {
	errAllRecords := errors.New("PR update needs all records")
	err := s.updateRecords([]string{key}, func(st *State) error {
		if needsAll(st) {
			return errAllRecords
		}
		return fn(st)
	})
	if errors.Is(err, errAllRecords) {
		// The full callback rechecks current state after reacquiring the lock.
		return s.update(fn)
	}
	return err
}

func finishStoreUpdate(fn func(*State) error) func(*State) error {
	return func(st *State) error {
		if err := fn(st); err != nil {
			return err
		}
		// Scoped callbacks expose only declared rows, so retirement and cache
		// cleanup cannot touch an unrelated record.
		finishUpdate(st)
		return nil
	}
}

func storeUpdateResult(err error) error {
	if errors.Is(err, errNoStoreChange) {
		return nil
	}
	return err
}

// finishUpdate retires notices and cached responses that ended with the change.
func finishUpdate(st *State) {
	for key, r := range st.Records {
		if !r.Watching || r.State != "open" {
			retireReviewNotices(r, "change-ended")
			// Runs on every save, so landing, closing, untracking and refreshes
			// all retire CI notices, including ones added after the change ended.
			retireCINotices(r, "change-ended")
		}
		// Closed records and previous heads are never polled again.
		if r.State != "open" {
			delete(st.Responses, key)
		} else {
			for path := range st.Responses[key] {
				if head := responseHead(path); head != "" && head != r.Head {
					delete(st.Responses[key], path)
				}
			}
		}
	}
}

// responseHead identifies the head-specific GitHub cache entries.
func responseHead(path string) string {
	endpoint, query, _ := strings.Cut(path, "?")
	if _, commit, ok := strings.Cut(endpoint, "/commits/"); ok {
		head, _, _ := strings.Cut(commit, "/")
		return head
	}
	values, _ := url.ParseQuery(query)
	if strings.HasSuffix(endpoint, "/actions/runs") {
		return values.Get("head_sha")
	}
	if fixedContent(path) {
		return values.Get("ref")
	}
	return ""
}

// Tests count real whole-state copies through the existing store boundaries.
var readStore = state.Read
var updateStore = state.Update

func (s *Store) Read() (State, error) { return readStore(s.Root) }

// OpenRecordsInRoom reads the unclosed PRs of one room without loading the
// whole PR snapshot.
func (s *Store) OpenRecordsInRoom(roomID string) ([]Record, error) {
	return state.OpenRecordsInRoom(s.Root, roomID)
}

// readRecords reads metadata and selected records from one committed revision.
// An empty selection keeps the full-read contract for callers that need all PRs.
func (s *Store) readRecords(keys ...string) (State, error) {
	if len(keys) == 0 {
		return s.Read()
	}
	snapshot, err := state.ReadSnapshot(s.Root)
	if err != nil {
		return State{}, err
	}
	st := snapshot.Metadata()
	st.Records = make(map[string]*Record, len(keys))
	for _, key := range keys {
		r, ok := snapshot.Record(key)
		if ok {
			st.Records[r.Key] = &r
		}
	}
	return st, nil
}

// readRecordResponses gives one record's saved responses, so a service call
// outside the poll workers can revalidate them instead of spending quota on
// bodies it already holds.
func (s *Store) readRecordResponses(key string) (map[string]cachedResponse, error) {
	snapshot, err := state.ReadSnapshot(s.Root)
	if err != nil {
		return nil, err
	}
	return snapshot.Responses(key), nil
}

func (s *Store) Get(key string) (Record, error) {
	snapshot, err := state.ReadSnapshot(s.Root)
	if err != nil {
		return Record{}, err
	}
	r, ok := snapshot.Record(key)
	if !ok {
		return Record{}, fmt.Errorf("PR is not tracked")
	}
	return r, nil
}

// ciNoticeTokens are the notice token prefixes of CI results and budgets.
var ciNoticeTokens = []string{"ci:", "ci-budget:", "test-budget:", "test-budget-policy:", "test-report-error:", "no-junit-report"}

func noticeKind(token string) string {
	if strings.HasPrefix(token, "informational:") {
		return "informational"
	}
	if strings.HasPrefix(token, "land:") {
		return "land"
	}
	if strings.HasPrefix(token, "radar:") {
		return "radar"
	}
	if strings.HasPrefix(token, "required-review:") {
		return "required-review"
	}
	for _, p := range ciNoticeTokens {
		if strings.HasPrefix(token, p) {
			return "ci"
		}
	}
	return ""
}

// retireCINotices drops undelivered CI notices once the change has ended.
// A stale "CI failed" must not reach the author after a merge or close.
func retireCINotices(r *Record, reason string) {
	for i := range r.Notices {
		n := &r.Notices[i]
		if n.Done || n.SupersededBy != "" {
			continue
		}
		if n.Kind == "ci" || legacyCINotice(n.Text) {
			n.SupersededBy = reason
		}
	}
}

// TODO: legacy/shim remove later — notices stored before Kind existed carry
// no kind. Match only a notice whose first body block is a CI result and
// that holds no GitHub comment, so a batched comment is never dropped.
func legacyCINotice(text string) bool {
	_, body, ok := strings.Cut(text, "\nHead: ")
	if !ok {
		return false
	}
	_, body, ok = strings.Cut(body, "\n\n")
	if !ok || strings.Contains(body, "\n\nGitHub ") {
		return false
	}
	return strings.HasPrefix(body, "CI failed (") || strings.HasPrefix(body, "CI for ") ||
		(strings.HasPrefix(body, "PR #") && strings.Contains(body, " failed CI three times in a row."))
}

// noticeFacts are the typed facts a notice carries into its PR event, so the
// web card reads them instead of the agent text.
type noticeFacts struct {
	Type      room.PRType
	Summary   string
	Commenter string
	Verdict   string
}

func notice(r *Record, target, text, token string, drops ...map[string]int) {
	noticeTyped(r, target, text, token, noticeFacts{}, drops...)
}

func noticeTyped(r *Record, target, text, token string, facts noticeFacts, drops ...map[string]int) {
	id := "msg_" + digest(r.Key + "/" + token + "/" + target)[:16]
	if token == "merge-conflict" {
		id = mergeNoticeID(*r, token)
	}
	for i := range r.Notices {
		n := &r.Notices[i]
		if n.ID == id {
			// A rerun can temporarily remove readiness on the same head, and
			// a queue order flip back can make a cleared radar notice true
			// again. Keep delivered notices deduplicated, but retry this pending one.
			// A required-review assignment retired only because the head moved
			// away, or the PR held as a draft, comes back when that same head or
			// the release asks for it again: the reviewer still owes the approval.
			if !n.Done && (n.SupersededBy == "no-longer-landable" || n.SupersededBy == "radar-cleared" ||
				(n.Kind == "required-review" && (strings.HasPrefix(n.SupersededBy, "new-head:") || strings.HasPrefix(n.SupersededBy, "head-changed:") || n.SupersededBy == "draft-hold"))) {
				n.SupersededBy = ""
			}
			// A notice saved before the pair identity existed still names its
			// pair once this pass matches it again.
			if n.Other == "" {
				n.Other = pairOther(token)
			}
			return
		}
	}

	var counts map[string]int
	if len(drops) > 0 {
		counts = drops[0]
	}
	if summary := dropSummary(counts); summary != "" {
		if i := strings.Index(text, "\n\nLog tail:"); i >= 0 {
			text = text[:i] + "\n\n" + summary + text[i:]
		} else {
			text += "\n\n" + summary
		}
	}
	if facts.Type == "" {
		facts.Type = typeOfToken(*r, token)
	}
	r.Notices = append(r.Notices, Notice{ID: id, Kind: noticeKind(token), Type: facts.Type, Summary: facts.Summary, Commenter: facts.Commenter, Verdict: facts.Verdict, Target: target, Other: pairOther(token), Text: boundNotice(changeLocation(*r) + "\nHead: " + r.Head + "\n\n" + text)})
}

// reviewNotice queues one typed review result post. The event is the fact the
// card draws; the text is the reviewer's own line, which the DM shows as the
// agent detail. One result per head and kind keeps its post on a retry.
func queueReviewNotice(r *Record, target, text, key string, ev room.ReviewEvent) {
	id := "msg_" + digest(r.Key + "/review:" + key + "/" + target)[:16]
	for i := range r.Notices {
		if r.Notices[i].ID == id {
			return
		}
	}
	r.Notices = append(r.Notices, Notice{ID: id, Kind: "review", Target: target, Review: &ev, Text: boundNotice(text)})
}

// typeOfToken reads the PR event type a notice write token names. A new notice
// carries its type already; the token keeps a caller from repeating it.
func typeOfToken(r Record, token string) room.PRType {
	switch {
	case token == "held-comment":
		return room.PRHeldComment
	case strings.HasPrefix(token, "merge-"), strings.HasPrefix(token, "local-merged"):
		return room.PRUpdate
	case strings.HasPrefix(token, "land:"):
		return room.PRReady
	case strings.HasPrefix(token, "informational:"), strings.HasPrefix(token, "radar:"):
		return room.PRUpdate
	case strings.HasPrefix(token, "verdict:"):
		if strings.HasSuffix(token, ":approve") || strings.HasSuffix(token, ":approve-with-nits") {
			return room.PRApproved
		}
		return room.PRChanges
	case strings.HasPrefix(token, "owner-comments:"), strings.HasPrefix(token, "review-comments:"):
		return room.PRComment
	case strings.HasPrefix(token, "transfer:reviewer:"), strings.HasPrefix(token, "required-review:"):
		return room.PRReviewAssigned
	case strings.HasPrefix(token, "ci-budget:"), strings.HasPrefix(token, "test-budget"),
		strings.HasPrefix(token, "test-report-error:"), strings.HasPrefix(token, "no-junit-report"):
		return room.PRUpdate
	case strings.HasPrefix(token, "ci:"), len(ciNoticeTokens) > 0 && slices.ContainsFunc(ciNoticeTokens, func(p string) bool { return strings.HasPrefix(token, p) }):
		return ciType(r)
	}
	return ""
}

// pairOther names the other change of a radar pair token.
func pairOther(token string) string {
	rest, ok := strings.CutPrefix(token, "radar:")
	if !ok {
		return ""
	}
	other, _, _ := strings.Cut(rest, ":")
	return other
}

// landable uses the same head and readiness facts as the merge worker.
func landable(st State, r Record, observed ...facts.GitHub) bool {
	g := facts.FromRecord(r)
	if len(observed) > 0 {
		g = observed[0]
	}
	return EffectivePolicy(st, r).Review != "off" && (facts.Ready(r, g, st.Records) || facts.ReadyWithoutHeadCI(r, g, st.Records))
}

func landLine(r Record) string {
	return fmt.Sprintf("PR #%d is ready to land at %s.", r.Number, r.Head)
}

// noticeIfLandable tells the author once per head that a change can land. It
// runs after CI and after a verdict, so the last one to arrive sends it.
// Delivery skips an author with a parent session; see landSkip.
func noticeIfLandable(st State, r *Record, observed ...facts.GitHub) {
	if landable(st, *r, observed...) {
		notice(r, r.Author, landLine(*r), "land:"+r.Head)
		return
	}
	// A fresh remote hold also retires readiness queued before that observation.
	for i := range r.Notices {
		n := &r.Notices[i]
		if n.Kind == "land" && !n.Done && n.SupersededBy == "" {
			n.SupersededBy = "no-longer-landable"
		}
	}
}
