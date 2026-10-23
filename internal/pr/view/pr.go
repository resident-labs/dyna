package view

import (
	"context"
	"crypto/sha256"
	"dyna-agent/internal/authority"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/listchange"
	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/subagent"
	"strings"
)

type PRConfig struct {
	MergeMaxParallel int    `json:"mergeMaxParallel"`
	GitHubAccess     string `json:"githubAccess"`
	ReviewPublishing string `json:"reviewPublishing"`
	ReviewerProfile  string `json:"reviewerProfile"`
	ReviewerSession  string `json:"reviewerSession"`
}

// Watcher uses PR state beside the existing session store.
func (s *Service) Watcher() *watch.Service {
	p := watch.New()
	if s.Now != nil {
		p.Now = s.Now
	}
	p.Notify = s.Notify
	p.GH.Rooms = s.Notify.Rooms
	p.Store = &watch.Store{Root: filepath.Join(s.dataDir(), "prs")}
	return p
}

func prStatus(r watch.Record, reviewState string) string {
	if r.State == "merged" || r.State == "closed" {
		return r.State
	}
	if !r.Watching {
		return "untracked"
	}
	if r.CIPaused || r.PauseReason != "" {
		return "paused"
	}
	if r.CI == "failed" {
		return "ci-failed"
	}
	if r.CI != "passed" && r.CI != "absent" {
		return "ci-running"
	}
	switch reviewState {
	case "approved":
		return "approved"
	case "changes":
		return "changes-requested"
	}
	return "awaiting-review"
}

func changeView(facts watch.ReviewFacts, r watch.Record) ChangeView {
	r = localCIChecks(facts, r)
	st := facts.State
	if r.Kind == "" {
		r.Kind = "github"
	}
	if r.ChangeID == "" {
		r.ChangeID = r.Key
	}
	reviewState, queuePosition, reviewBlocked := facts.ReviewState(r)
	// Decisions are keyed by comment/review ID. Count the latest decision
	// for each item, including edits, rather than transient pending notices.
	filtered := 0
	for _, d := range r.CommentDecisions {
		if d.Action == "drop" {
			filtered++
		}
	}
	checks := r.Checks
	if checks == nil {
		checks = []watch.CheckSummary{}
	}
	botReviews := append([]state.BotReview{}, r.BotReviews...)
	reviewURL := ""
	if r.ReviewID > 0 {
		reviewURL = fmt.Sprintf("%s#pullrequestreview-%d", r.URL, r.ReviewID)
	}
	errorText := r.Error
	for _, n := range r.Notices {
		if !n.Done && n.SupersededBy == "" && n.Error != "" {
			if errorText != "" {
				errorText += "\n"
			}
			errorText += "Delivery: " + n.Error
			break
		}
	}
	var reviewAt time.Time
	if n := len(r.ReviewRounds); n > 0 {
		reviewAt = r.ReviewRounds[n-1].At
	}
	detailJSON, _ := json.Marshal(struct {
		changeDetails
		RequiredReviewers []string `json:"requiredReviewers,omitempty"`
	}{detailsOf(st, r), r.RequiredReviewers})
	detailVersion := fmt.Sprintf("%x", sha256.Sum256(detailJSON))
	return ChangeView{HeldCommentCount: len(r.HeldComments), GoalID: r.GoalID, RequiredReviewers: slices.Clone(r.RequiredReviewers), MissingRequiredReviewers: prfacts.MissingRequiredReviewers(r), IsDraft: r.IsDraft, DetailVersion: detailVersion, HasPolicyOverride: !r.PolicyOverride.Empty(), ChangeID: r.ChangeID, Kind: r.Kind, ReviewState: reviewState, QueuePosition: queuePosition, ReviewBlocked: reviewBlocked, Branch: r.Branch, BaseRef: r.BaseRef, Base: r.Base, Key: r.Key, Repo: r.Repo, Number: r.Number, URL: r.URL, Title: r.Title, RoomID: r.RoomID, AuthorSession: r.Author, ReviewerSession: r.Reviewer, ReviewerMode: r.ReviewerMode, ReviewPublishing: reviewPublishing(r), ReviewerName: r.ReviewerName, State: r.State, Watching: r.Watching, Status: prStatus(r, reviewState), CI: r.CI, CIReason: r.CIReason, CITestReportNote: r.CITestReportNote, BotReviews: botReviews, Checks: checks, ConsecutiveFailures: r.Failures, CIPaused: r.CIPaused, PauseReason: r.PauseReason, Error: errorText, ReviewRound: r.Round, ReviewHead: r.RoundHead, Verdict: r.Verdict, ReviewAt: reviewAt, FindingCount: len(r.Findings), HasReviewSummary: r.Summary != "", ReviewURL: reviewURL, Head: r.Head, Created: r.Created, Checked: r.Checked, FilteredComments: filtered, Overlaps: state.SharedOverlaps(r.Overlaps)}
}

// A historical GitHub ID records a public review, regardless of today's default.
func reviewPublishing(r watch.Record) string {
	if r.ReviewID > 0 {
		return watch.PublishingGitHub
	}
	return watch.PublishingMode(r.ReviewPublishing)
}

func (s *Service) PRSettings(roomRef string) (PRConfig, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return PRConfig{}, err
	}
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return PRConfig{}, err
	}
	cfg := st.Reviewers[rec.ID]
	settings, err := s.Watcher().MergeSettings(rec.ID)
	if err != nil {
		return PRConfig{}, err
	}
	return PRConfig{MergeMaxParallel: settings.MaxParallel, GitHubAccess: ghclient.Access(context.Background(), rec), ReviewPublishing: watch.PublishingMode(cfg.ReviewPublishing), ReviewerProfile: cfg.Profile}, nil
}

func (s *Service) SetPRSettings(ctx context.Context, roomRef string, cfg PRConfig) (PRConfig, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return PRConfig{}, err
	}
	if err := checkIn(ctx, "SetPRSettings", rec.ID); err != nil {
		return PRConfig{}, err
	}
	if cfg.MergeMaxParallel < 0 {
		return PRConfig{}, fmt.Errorf("merge max-parallel must be a positive integer")
	}
	if cfg.ReviewerSession != "" {
		return PRConfig{}, fmt.Errorf("reviewer sessions are managed by Dyna; choose a profile or auto")
	}
	if cfg.GitHubAccess != "" {
		if err := ghclient.ValidateAccess(cfg.GitHubAccess); err != nil {
			return PRConfig{}, err
		}
	}
	p := s.Watcher()
	if err := p.ConfigureRoom(rec.ID, watch.Reviewer{Profile: cfg.ReviewerProfile, ReviewPublishing: cfg.ReviewPublishing}); err != nil {
		return PRConfig{}, err
	}
	if cfg.GitHubAccess != "" {
		if err := ghclient.SetAccess(s.Notify.Rooms, rec.ID, cfg.GitHubAccess); err != nil {
			return PRConfig{}, err
		}
		rec.GitHubAccess = cfg.GitHubAccess
	}
	if cfg.MergeMaxParallel > 0 {
		if err := p.SetMergeMaxParallel(rec.ID, cfg.MergeMaxParallel); err != nil {
			return PRConfig{}, err
		}
	}
	settings, err := p.MergeSettings(rec.ID)
	if err != nil {
		return PRConfig{}, err
	}
	cfg.MergeMaxParallel = settings.MaxParallel
	cfg.GitHubAccess = ghclient.Access(context.Background(), rec)
	cfg.ReviewPublishing = watch.PublishingMode(cfg.ReviewPublishing)
	return cfg, nil
}

// TransferPR moves a PR's author role to another open session in its room.
// dir is the caller's checkout of the PR's repository, or "".
func (s *Service) TransferPR(ctx context.Context, roomRef, key, role, toRef, dir string) (watch.Record, error) {
	p, _, err := s.authorPRInRoom(ctx, "git pr transfer", roomRef, key)
	if err != nil {
		return watch.Record{}, err
	}
	r, err := p.Store.Get(key)
	if err != nil {
		return watch.Record{}, err
	}
	to, err := s.SessionInRoom(r.RoomID, toRef)
	if err != nil {
		return watch.Record{}, err
	}
	if dir != "" {
		rec, err := s.Notify.Rooms.Resolve(r.RoomID)
		if err != nil {
			return watch.Record{}, err
		}
		if err := CheckoutOfRoom(ctx, rec, dir); err != nil {
			return watch.Record{}, err
		}
	}
	if err := p.Transfer(ctx, key, role, to, dir); err != nil {
		return watch.Record{}, err
	}
	return p.Store.Get(key)
}

// SetPRDraft holds a PR as a GitHub draft with a reason, or releases it.
func (s *Service) SetPRDraft(ctx context.Context, roomRef, key string, draft bool, reason string) error {
	action := "git pr release"
	if draft {
		action = "git pr hold"
	}
	p, _, err := s.authorPRInRoom(ctx, action, roomRef, key)
	if err != nil {
		return err
	}
	return p.SetDraft(ctx, key, draft, reason)
}

func (s *Service) UntrackPR(ctx context.Context, roomRef, key string) error {
	p, _, err := s.authorPRInRoom(ctx, "git pr untrack", roomRef, key)
	if err != nil {
		return err
	}
	return p.Untrack(ctx, key)
}

func (s *Service) ResumePR(ctx context.Context, roomRef, key string) error {
	p, _, err := s.authorPRInRoom(ctx, "git pr resume", roomRef, key)
	if err != nil {
		return err
	}
	return p.Resume(key)
}

// checkIn runs action's rule for the caller acting in roomID.
func checkIn(ctx context.Context, action, roomID string) error {
	actor, err := authority.ActorIn(ctx, roomID)
	if err != nil {
		return err
	}
	return authority.Check(action, actor, nil)
}

// authorPRInRoom is prInRoom for a change that only its author, the owner or
// root may change. It returns the record it checked, whose Author a write
// passes on so the write refuses if the author changed in between.
func (s *Service) authorPRInRoom(ctx context.Context, action, roomRef, key string) (*watch.Service, watch.Record, error) {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return nil, watch.Record{}, err
	}
	r, err := p.Store.Get(key)
	if err != nil {
		return nil, watch.Record{}, err
	}
	actor, err := authority.ActorIn(ctx, r.RoomID)
	if err != nil {
		return nil, watch.Record{}, err
	}
	// The author or an ancestor of the author's session acts on the PR, by the
	// same rule as the session controls (#1605), so the two paths cannot differ.
	actsOnAuthor := func() bool {
		return actor.Session != "" && r.Author != "" && s.Notify.Sessions.CheckActsOn(actor.Session, subagent.Info{Meta: subagent.Meta{ID: r.Author}}) == nil
	}
	// authority.Check reads the Author relation for sessions only; a steward
	// acts on its own tree exactly as #1605's checkActsOn lets it.
	if actor.Role == authority.Steward && actsOnAuthor() {
		return p, r, nil
	}
	// The author holds the PR's one transferable role, so Holder is Author.
	if err := authority.Check(action, actor, func(rel authority.Relation) bool {
		return (rel == authority.Author || rel == authority.Holder) && actsOnAuthor()
	}); err != nil {
		return nil, watch.Record{}, err
	}
	return p, r, nil
}

func (s *Service) prInRoom(roomRef, key string) (*watch.Service, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	p := s.Watcher()
	r, err := p.Store.Get(key)
	if err != nil {
		return nil, err
	}
	if r.RoomID != rec.ID {
		return nil, fmt.Errorf("PR #%d is not in this room", r.Number)
	}
	return p, nil
}

// Scan reads only after the PR state revision changes, even when many
// clients watch the same quiet room: an idle call reads one row. It rebuilds
// rows only for records written since the last scan and for records whose
// place in a list head changed, and reports moved or removed records
// deleted. The first scan, or one after a reset (version ""), rebuilds
// every row. Settings changes emit the same event.
func (s *Service) Scan(signatures, work map[string]string, version *string, first bool, emit func(kind, roomID string, change *listchange.Change, workerWake bool)) {
	projection, err := s.readProjection()
	if err != nil {
		return
	}
	current := projection.revision
	dependency := projection.dependency()
	if current == *version && work["\x00projection"] == dependency {
		return
	}
	if *version != "" && work["\x00view"] == projection.version && work["\x00projection"] == dependency {
		// Poll-only commits need no row or worker work. Advance the raw
		// cursor so a later visible change queries only its own delta.
		*version = current
		return
	}
	st := projection.state
	var changed *recordChanges
	// A new dependency needs every row, so a delta query would be discarded.
	// Unchanged PR revisions never query the changed-key index.
	if work["\x00projection"] == dependency {
		changed, err = changedRecords(projection.root, *version, st, signatures)
		if err != nil {
			return
		}
	}
	// Worker-input text is kept for records that did not change. A full
	// rebuild makes all of it again, and a removed record drops its text.
	if changed == nil {
		maps.DeleteFunc(work, func(key, _ string) bool { return strings.HasPrefix(key, inputPrefix) })
	} else {
		for _, key := range changed.gone {
			if parts := strings.SplitN(key, "\x00", 3); len(parts) == 3 {
				delete(work, inputPrefix+parts[2])
			}
		}
	}
	rooms := map[string]bool{}
	byRoom := map[string][]*watch.Record{}
	for _, record := range st.Records {
		rooms[record.RoomID] = true
		byRoom[record.RoomID] = append(byRoom[record.RoomID], record)
	}
	for roomID := range st.Policies {
		rooms[roomID] = true
	}
	for roomID := range st.Reviewers {
		rooms[roomID] = true
	}
	for roomID := range work {
		if !strings.HasPrefix(roomID, "\x00") {
			rooms[roomID] = true
		}
	}
	rows := []listchange.Item{}
	built := map[string]ChangeView{}
	wake, metadata := map[string]bool{}, map[string]bool{}
	for roomID := range rooms {
		data, _ := json.Marshal(struct {
			Settings watch.Reviewer
			Policy   watch.ChangePolicy
		}{st.Reviewers[roomID], watch.RoomPolicy(st, roomID)})
		key := "\x00metadata\x00" + roomID
		metadata[roomID] = work[key] != string(data)
		work[key] = string(data)
		dirty := changed == nil || changed.rooms[roomID]
		// Worker inputs come from the room's records and settings only.
		if !dirty && !metadata[roomID] {
			continue
		}
		for _, list := range []struct {
			kind    string
			prsOnly bool
		}{{"prs", true}, {"changes", false}} {
			if !dirty {
				break
			}
			page, err := projection.page(roomID, "", 0, list.prsOnly)
			if err != nil {
				return
			}
			head := listchange.HeadIDs(page.Items, func(r *watch.Record) string { return r.Key })
			var items []ChangeView
			for _, r := range byRoom[roomID] {
				if list.prsOnly && r.Kind == "local" {
					continue
				}
				if changed != nil && !changed.keys[r.Key] && !listchange.HeadChanged(signatures[listchange.Key(list.kind, roomID, r.Key)], head[r.Key]) {
					continue
				}
				row, ok := built[r.Key]
				if !ok {
					row = projection.row(r)
					built[r.Key] = row
				}
				items = append(items, row)
			}
			rows = append(rows, publishedRows(list.kind, roomID, items, head)...)
		}
		input := scanWorkerInputs(st, roomID, byRoom[roomID], work, func(key string) bool { return changed == nil || changed.keys[key] })
		wake[roomID] = work[roomID] != input
		work[roomID] = input
	}
	var changes []listchange.Event
	if changed == nil {
		changes, err = listchange.Sync(signatures, rows, first)
	} else {
		changes, err = listchange.Patch(signatures, rows, changed.gone)
	}
	if err != nil {
		return
	}
	*version = current
	work["\x00projection"] = dependency
	work["\x00view"] = projection.version
	emitted := map[string]bool{}
	for _, event := range changes {
		value := event.Change
		emit(event.Kind, event.RoomID, &value, wake[event.RoomID] && !emitted[event.RoomID])
		emitted[event.RoomID] = true
	}
	if !first {
		for roomID := range rooms {
			if !emitted[roomID] && (metadata[roomID] || wake[roomID]) {
				emit("prs", roomID, nil, wake[roomID])
			}
		}
	}
}

// buildRow makes one record's row. Tests count calls through it.
var buildRow = changeView

// recordChanges is what changed since a scan's last revision.
type recordChanges struct {
	keys  map[string]bool // records written since
	rooms map[string]bool // rooms of written, moved or removed records
	gone  []string        // published rows whose record is removed or moved
}

// changedRecords finds the records written since revision since, and the
// published rows whose record is gone or now in another room. It returns
// nil, meaning every row, for the first scan or after a reset.
//
// A row must depend only on its PR record, the room's settings and policy,
// and review facts. A fact kept outside pr_records, for example a cache
// written elsewhere, must bump the PR state revision or mark its records
// written, or its rows go stale without notice.
// Tests count calls to the actual changed-key query.
var keysChangedSince = state.KeysChangedSince

func changedRecords(root, since string, st watch.State, signatures map[string]string) (*recordChanges, error) {
	if since == "" || since == "missing" || len(signatures) == 0 {
		return nil, nil
	}
	written, err := keysChangedSince(root, since)
	if err != nil {
		return nil, err
	}
	c := &recordChanges{keys: map[string]bool{}, rooms: map[string]bool{}}
	for _, key := range written {
		if r := st.Records[key]; r != nil {
			c.keys[key] = true
			c.rooms[r.RoomID] = true
		}
	}
	for key := range signatures {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		if r := st.Records[parts[2]]; r == nil || r.RoomID != parts[1] {
			c.gone = append(c.gone, key)
			c.rooms[parts[1]] = true
		}
	}
	return c, nil
}

// publishedRows compares rows without Checked. A poll time alone is no new
// content, and republishing every polled row made each app reload its PRs.
func publishedRows(kind, roomID string, rows []ChangeView, head map[string]bool) []listchange.Item {
	items := listchange.Rows(kind, roomID, rows, head, func(row ChangeView) string { return row.Key })
	for i, row := range rows {
		row.Checked = time.Time{}
		data, _ := json.Marshal(row)
		items[i].Version = string(data)
	}
	return items
}

func (s *Service) ReviewerStatus(roomRef string) (watch.ReviewerStatus, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return watch.ReviewerStatus{}, err
	}
	return s.Watcher().ReviewerStatus(rec.ID)
}

// SetReviewerProfile pins the room's reviewer to a profile; "" means Auto.
func (s *Service) SetReviewerProfile(ctx context.Context, roomRef, profile string) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "git pr reviewer", rec.ID); err != nil {
		return err
	}
	return s.Watcher().SetReviewerProfile(rec.ID, profile)
}
func (s *Service) SetReviewEnabled(ctx context.Context, roomRef string, enabled bool) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "git pr review", rec.ID); err != nil {
		return err
	}
	return s.Watcher().SetReviewEnabled(rec.ID, enabled)
}

// MergeFixPR merges one approved, green PR while main CI is red. The owner or
// the room's root asks for it; the watcher records who did.
func (s *Service) MergeFixPR(ctx context.Context, roomRef, key string) error {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return err
	}
	r, err := p.Store.Get(key)
	if err != nil {
		return err
	}
	actor, err := authority.ActorIn(ctx, r.RoomID)
	if err != nil {
		return err
	}
	// A merge that started must finish, even if the caller's request ends.
	return p.MergeFix(authority.WithActor(context.WithoutCancel(ctx), actor), key)
}

// SetGitHubAccess sets the room's GitHub access, read-only or read-write; the
// owner or the room's root may set it.
func (s *Service) SetGitHubAccess(ctx context.Context, roomRef, access string) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "SetGitHubAccess", rec.ID); err != nil {
		return err
	}
	return ghclient.SetAccess(s.Notify.Rooms, rec.ID, access)
}

// SetMergeSettings sets the room's maximum number of parallel merge trials.
func (s *Service) SetMergeSettings(ctx context.Context, roomRef string, maxParallel int) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "git pr merge-settings", rec.ID); err != nil {
		return err
	}
	return s.Watcher().SetMergeMaxParallel(rec.ID, maxParallel)
}

// SetReviewPublishing sets where the room's future reviews are published.
func (s *Service) SetReviewPublishing(ctx context.Context, roomRef, value string) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "git pr publishing", rec.ID); err != nil {
		return err
	}
	return s.Watcher().ConfigurePublishing(rec.ID, value)
}
func (s *Service) RestartReviewer(ctx context.Context, roomRef string) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "git pr reviewer", rec.ID); err != nil {
		return err
	}
	// A restart closes the room's reviewers and then starts one; the end of
	// the caller's request must not stop it between the two.
	return s.Watcher().RestartReviewer(context.WithoutCancel(ctx), rec.ID)
}
