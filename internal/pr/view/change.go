package view

import (
	"context"
	"dyna-agent/internal/listpage"
	"dyna-agent/internal/notify"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

type ChangeView struct {
	GoalID                   string   `json:"goalId,omitempty"`
	RequiredReviewers        []string `json:"requiredReviewers,omitempty"`
	MissingRequiredReviewers []string `json:"missingRequiredReviewers,omitempty"`
	IsDraft                  bool     `json:"isDraft"`
	DetailVersion            string   `json:"detailVersion"`
	HasPolicyOverride        bool     `json:"hasPolicyOverride,omitempty"`
	ChangeID                 string   `json:"changeId,omitempty"`
	Kind                     string   `json:"kind,omitempty"`
	Branch                   string   `json:"branch,omitempty"`
	BaseRef                  string   `json:"baseRef,omitempty"`
	Base                     string   `json:"base,omitempty"`
	ReviewState              string   `json:"reviewState,omitempty"`
	QueuePosition            *int     `json:"queuePosition"`
	ReviewBlocked            string   `json:"reviewBlocked,omitempty"`

	Key                 string               `json:"key,omitempty"`
	Repo                string               `json:"repo,omitempty"`
	Number              int                  `json:"number,omitempty"`
	URL                 string               `json:"url,omitempty"`
	Title               string               `json:"title,omitempty"`
	RoomID              string               `json:"roomId,omitempty"`
	AuthorSession       string               `json:"authorSession,omitempty"`
	ReviewerSession     string               `json:"reviewerSession,omitempty"`
	HeldCommentCount    int                  `json:"heldCommentCount"`
	ReviewPublishing    string               `json:"reviewPublishing,omitempty"`
	ReviewerMode        string               `json:"reviewerMode,omitempty"`
	ReviewerName        string               `json:"reviewerName,omitempty"`
	State               string               `json:"state,omitempty"`
	Watching            bool                 `json:"watching,omitempty"`
	Status              string               `json:"status,omitempty"`
	CI                  string               `json:"ci,omitempty"`
	CITestReportNote    string               `json:"ciTestReportNote,omitempty"`
	CIReason            string               `json:"ciReason,omitempty"`
	BotReviews          []state.BotReview    `json:"botReviews"`
	Checks              []watch.CheckSummary `json:"checks"`
	ConsecutiveFailures int                  `json:"consecutiveFailures,omitempty"`
	CIPaused            bool                 `json:"ciPaused,omitempty"`
	PauseReason         string               `json:"pauseReason,omitempty"`
	Error               string               `json:"error,omitempty"`
	ReviewRound         int                  `json:"reviewRound,omitempty"`
	ReviewHead          string               `json:"reviewHead,omitempty"`
	Verdict             string               `json:"verdict,omitempty"`
	ReviewAt            time.Time            `json:"reviewAt,omitzero"`
	FindingCount        int                  `json:"findingCount,omitempty"`
	HasReviewSummary    bool                 `json:"hasReviewSummary,omitempty"`
	ReviewURL           string               `json:"reviewUrl,omitempty"`
	Head                string               `json:"head,omitempty"`
	Created             time.Time            `json:"created,omitempty"`
	Checked             time.Time            `json:"checked,omitempty"`
	FilteredComments    int                  `json:"filteredComments,omitempty"`
	Overlaps            []state.Overlap      `json:"overlaps,omitempty"`
}

type ChangeDetail struct {
	ChangeView
	changeDetails
}

type changeDetails struct {
	Summary        string               `json:"summary"`
	Notes          state.AuthorNotes    `json:"notes"`
	Findings       []watch.Finding      `json:"findings"`
	ReviewRounds   []state.ReviewRound  `json:"reviewRounds"`
	Policy         watch.ChangePolicy   `json:"policy"`
	PolicyOverride watch.PolicyOverride `json:"policyOverride"`
	SkipReasons    []string             `json:"skipReasons"`
}

func (s *Service) ChangeDetail(roomRef, key string) (ChangeDetail, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return ChangeDetail{}, err
	}
	projection, err := s.readProjection()
	if err != nil {
		return ChangeDetail{}, err
	}
	st := projection.state
	r := st.Records[key]
	if r == nil || r.RoomID != rec.ID {
		return ChangeDetail{}, fmt.Errorf("change is not in this room")
	}
	detail := ChangeDetail{ChangeView: projection.row(r), changeDetails: detailsOf(st, *r)}
	// Details also contain nested policy and review slices owned by state.
	data, err := json.Marshal(detail)
	if err != nil {
		return ChangeDetail{}, err
	}
	var out ChangeDetail
	err = json.Unmarshal(data, &out)
	return out, err
}

func detailsOf(st watch.State, r watch.Record) changeDetails {
	policy := watch.EffectivePolicy(st, r)
	return changeDetails{Summary: r.Summary, Notes: r.Notes, Findings: append([]watch.Finding{}, r.Findings...), ReviewRounds: append([]state.ReviewRound{}, r.ReviewRounds...), Policy: policy, PolicyOverride: r.PolicyOverride, SkipReasons: changepolicy.SkipReasons(policy, r.PolicyOverride)}
}

func (s *Service) Changes(roomRef, before string, limit int) (listpage.Page[ChangeView], error) {
	roomID := ""
	if roomRef != "" {
		room, err := s.Notify.Rooms.Resolve(roomRef)
		if err != nil {
			return listpage.Page[ChangeView]{}, err
		}
		roomID = room.ID
	}
	projection, err := s.readProjection()
	if err != nil {
		return listpage.Page[ChangeView]{}, err
	}
	page, err := projection.page(roomID, before, limit, false)
	if err != nil {
		return listpage.Page[ChangeView]{}, err
	}
	rows := make([]ChangeView, 0, len(page.Items))
	for _, r := range page.Items {
		rows = append(rows, projection.row(r))
	}
	return listpage.Page[ChangeView]{Items: rows, Older: page.Older, Cursor: page.Cursor}, nil
}
func sortedChanges(facts watch.ReviewFacts, roomID string, records map[string]*watch.Record) []ChangeView {
	return changeRows(facts, roomID, records, false)
}

func changeRows(facts watch.ReviewFacts, roomID string, records map[string]*watch.Record, prsOnly bool) []ChangeView {
	out := []ChangeView{}
	for _, r := range records {
		if (roomID != "" && r.RoomID != roomID) || (prsOnly && r.Kind == "local") {
			continue
		}
		row := buildRow(facts, *r)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// RoomMergeStatus is the room's merge queue: one state and reason per open
// PR, in the order the merge worker uses. It reads stored facts only.
func (s *Service) RoomMergeStatus(roomRef string) ([]watch.MergeQueueItem, error) {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	return s.Watcher().RoomMergeQueue(room.ID)
}

// RoomMergeHistory lists the room's newest merge trial results and publishes.
func (s *Service) RoomMergeHistory(roomRef string) ([]watch.MergeHistoryEntry, error) {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	return s.Watcher().RoomMergeHistory(room.ID)
}

// ReplyToFinding records a reply to a finding. The author, its ancestors, the
// owner and the room's root reply; only the owner or root resolves a finding.
func (s *Service) ReplyToFinding(ctx context.Context, roomRef, changeID, findingID, disposition, message string) error {
	var p *watch.Service
	var r watch.Record
	var err error
	if disposition == "resolved" {
		room, err := s.Notify.Rooms.Resolve(roomRef)
		if err != nil {
			return err
		}
		if p, err = s.prInRoom(room.ID, changeID); err != nil {
			return err
		}
		if err := checkIn(ctx, "ResolveFinding", room.ID); err != nil {
			return err
		}
		if r, err = p.Store.Get(changeID); err != nil {
			return err
		}
	} else if p, r, err = s.authorPRInRoom(ctx, "git change reply", roomRef, changeID); err != nil {
		return err
	}
	// Any session's reply is its own, root seat and stewards included; only
	// the owner replies with no session.
	return p.ReplyFinding(ctx, changeID, r.Author, authority.Of(ctx).Session, findingID, disposition, message)
}

// CloseChange stops watching a change; its author, the author's ancestors,
// the owner and the room's root may close it.
func (s *Service) CloseChange(ctx context.Context, roomRef, changeID string) error {
	p, r, err := s.authorPRInRoom(ctx, "git change close", roomRef, changeID)
	if err != nil {
		return err
	}
	return p.CloseChange(changeID, r.Author)
}

// SendDiffComments stores the owner's diff comments as open findings and tells the author once.
func (s *Service) SendDiffComments(ctx context.Context, roomRef, changeID string, comments []watch.DiffComment) error {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "SendDiffComments", room.ID); err != nil {
		return err
	}
	p := s.Watcher()
	r, err := p.Store.Get(changeID)
	if err != nil {
		return err
	}
	if r.RoomID != room.ID {
		return fmt.Errorf("change is not in this room")
	}
	return p.SendDiffComments(ctx, r.Key, comments)
}

func sameDir(a, b string) bool { return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b) }

// SendWorktreeComments sends the owner's comments on a session's worktree.
// With an open change by that session they become owner findings; without
// one the same text goes to the session as a post.
func (s *Service) SendWorktreeComments(ctx context.Context, roomRef, sessionRef string, comments []watch.DiffComment) error {
	info, err := s.SessionInRoom(roomRef, sessionRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "SendWorktreeComments", info.RoomID); err != nil {
		return err
	}
	p := s.Watcher()
	st, err := p.Store.Read()
	if err != nil {
		return err
	}
	// The change for this worktree: opened by the session, from the session's directory.
	var keys []string
	for _, r := range st.Records {
		if r.Author == info.ID && r.RoomID == info.RoomID && r.State == "open" && r.Watching && sameDir(r.Dir, info.Dir) {
			keys = append(keys, r.Key)
		}
	}
	if len(keys) > 1 {
		return fmt.Errorf("this worktree has %d open changes; close the extra ones first", len(keys))
	}
	if len(keys) == 1 {
		return p.SendDiffComments(context.Background(), keys[0], comments)
	}
	rec, err := s.Notify.Rooms.Resolve(info.RoomID)
	if err != nil {
		return err
	}
	_, err = s.Notify.Post(context.Background(), notify.PostInput{Sender: notify.Sender{Kind: notify.SenderOwner, Room: rec.Name, Human: true}, Mentions: []string{"@" + info.Address()}, Text: watch.FormatDiffComments(comments)})
	return err
}

func pageRecords(st watch.State, roomID, before string, limit int, prsOnly bool, now time.Time) (listpage.Page[*watch.Record], error) {
	records := []*watch.Record{}
	for _, r := range st.Records {
		if (roomID == "" || r.RoomID == roomID) && (!prsOnly || r.Kind != "local") {
			records = append(records, r)
		}
	}
	return listpage.Select(records, before, limit, now, func(r *watch.Record) listpage.Meta {
		at := r.ClosedAt
		if r.State == "open" {
			at = r.Created
		}
		return listpage.Meta{ID: r.Key, At: at, Open: r.State == "open"}
	})
}
