package roomsvc

import (
	"strings"
	"unicode/utf8"

	"context"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"log"
	"slices"
	"sort"
	"strconv"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/listpage"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/transcript"
	"dyna-agent/internal/workeroutput"
)

// Session is a member's drill-down: identity plus transcript.
type Session struct {
	Cursor  string             `json:"cursor,omitempty"`
	Earlier bool               `json:"earlier"`
	Newer   bool               `json:"newer"`
	Member  Member             `json:"member"`
	Address string             `json:"address"`
	Posts   []Post             `json:"posts"`
	Media   []Post             `json:"media"`
	Dir     string             `json:"dir"`
	Usage   workeroutput.Usage `json:"usage"`
	Turns   []Turn             `json:"turns"`
}

// Turn is one transcript turn.
type Turn struct {
	Harness            string                          `json:"harness,omitempty"`
	OutputDialect      string                          `json:"outputDialect,omitempty"`
	UsageNormalization workeroutput.UsageNormalization `json:"usageNormalization,omitempty"`
	Profile            string                          `json:"profile,omitempty"`
	Model              string                          `json:"model,omitempty"`
	HarnessSession     string                          `json:"harnessSession,omitempty"`
	N                  int                             `json:"n"`
	Status             string                          `json:"status"`
	Started            time.Time                       `json:"started"`
	Ended              time.Time                       `json:"ended"`
	Usage              workeroutput.Usage              `json:"usage"`
	Items              []Item                          `json:"items"`
	Omitted            bool                            `json:"omitted,omitempty"`
}

// Item is one transcript entry. Kind is prompt, steering, reply, tool,
// thought, error, or note.
type Item struct {
	PostID    string    `json:"postId,omitempty"`
	Delivered bool      `json:"delivered,omitempty"`
	Kind      string    `json:"kind"`
	TS        time.Time `json:"ts"`
	Text      string    `json:"text,omitempty"`
	From      string    `json:"from,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Target    string    `json:"target,omitempty"`
	Aside     string    `json:"aside,omitempty"`
	Failed    bool      `json:"failed,omitempty"`
	Done      bool      `json:"done,omitempty"`
	// JobID and JobOp link a call of Dyna's shell tools to its job: JobOp is
	// run, output or stop. Job is the snapshot of the job a run started.
	// Command, Output and Exit explain a failed call: its input, the tail of
	// what it printed, and its exit code. Other calls carry none of them.
	Command string                `json:"command,omitempty"`
	Output  string                `json:"output,omitempty"`
	Exit    *int                  `json:"exit,omitempty"`
	JobID   string                `json:"jobId,omitempty"`
	JobOp   string                `json:"jobOp,omitempty"`
	Job     *terminalsvc.ShellJob `json:"job,omitempty"`
	// A compaction item carries its state, its trigger (auto, manual or dyna),
	// the context size on each side when reported, and the reason it failed.
	State        string `json:"state,omitempty"`
	Trigger      string `json:"trigger,omitempty"`
	TokensBefore int64  `json:"tokensBefore,omitempty"`
	TokensAfter  int64  `json:"tokensAfter,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

var kindNames = map[transcript.Kind]string{
	transcript.Prompt: "prompt", transcript.Steering: "steering", transcript.Reply: "reply",
	transcript.ToolUse: "tool", transcript.Thought: "thought", transcript.Error: "error", transcript.Note: "note",
	transcript.Compaction: "compaction",
}

const (
	maxFailedCommand = 1000
	maxFailedOutput  = 2000
)

// tail keeps the last max bytes of s, cut on a character, and marks the cut.
func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "…" + s[cut:]
}

func sessionItem(it transcript.Item) Item {
	item := Item{Kind: kindNames[it.Kind], TS: it.TS, Text: it.Text, From: it.From, PostID: it.PostID, Delivered: it.Delivered}
	switch it.Kind {
	case transcript.ToolUse:
		item.Tool, item.Target = transcript.Headline(it.Tool), it.Tool.Target
		item.Aside, item.Failed, item.Done = transcript.ToolAside(it.Tool), it.Tool.Failed, it.Tool.Done
		item.JobID, item.JobOp = it.Tool.JobID, it.Tool.JobOp
		if it.Tool.Failed {
			item.Command, item.Output, item.Exit = tail(it.Tool.Detail, maxFailedCommand), tail(ansi.Strip(it.Tool.Output), maxFailedOutput), it.Tool.Exit
		}
	case transcript.Thought:
		item.Aside = transcript.ThoughtAside(it)
	case transcript.Compaction:
		c := it.Compaction
		item.State, item.Trigger, item.Reason = string(c.State), c.Trigger, c.Reason
		item.TokensBefore, item.TokensAfter = c.Before, c.After
	}
	return item
}

// Transcript reads a session's six newest turns.
func (s *Service) Transcript(ref string) (Session, error) { return s.TranscriptMore(ref, 6) }

// TranscriptMore reads at least six recent turns.
func (s *Service) TranscriptMore(ref string, lastTurns int) (Session, error) {
	if lastTurns < 6 {
		lastTurns = 6
	}
	return s.transcript(ref, lastTurns, 0)
}

// TranscriptPage returns a bounded page in turn order. An empty cursor selects
// the newest turns; before selects turns older than that turn number.
func (s *Service) TranscriptPage(ref, before string, limit int) (Session, error) {
	return s.transcriptPage(ref, before, limit, false)
}

// TranscriptAfter reads a bounded page newer than an exclusive turn number.
func (s *Service) TranscriptAfter(ref, after string, limit int) (Session, error) {
	return s.transcriptPage(ref, after, limit, true)
}

func (s *Service) transcriptPage(ref, cursor string, limit int, forward bool) (Session, error) {
	n := 0
	if cursor != "" {
		var err error
		n, err = strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return Session{}, fmt.Errorf("invalid turn cursor")
		}
	}
	read := s.transcriptLoader().LoadPage
	if forward {
		read = s.transcriptLoader().LoadAfter
	}
	sess, err := read(ref, n, listpage.Limit(limit))
	if err != nil {
		return Session{}, err
	}
	if cursor != "" && len(sess.Turns) == 0 {
		return Session{Member: s.member(sess.Info, 0, 0), Address: sess.Info.Address(), Earlier: sess.Omitted, Newer: sess.Newer, Posts: []Post{}, Turns: []Turn{}}, nil
	}
	return s.sessionView(sess)
}

func (s *Service) transcriptLoader() *transcript.Loader {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loader == nil {
		s.loader = transcript.NewLoader(s.sessions())
	}
	return s.loader
}

// TranscriptTurn opens the exact turn referenced by a completion post.
func (s *Service) TranscriptTurn(ref string, n int) (Session, error) {
	if n < 1 {
		return Session{}, fmt.Errorf("turn must be positive")
	}
	return s.transcript(ref, 0, n)
}

func (s *Service) transcript(ref string, lastTurns, n int) (Session, error) {
	loader := s.transcriptLoader()
	var sess transcript.Session
	var err error
	if n > 0 {
		sess, err = loader.LoadTurn(ref, n)
	} else {
		sess, err = loader.Load(ref, lastTurns)
	}
	if err != nil {
		return Session{}, err
	}
	return s.sessionView(sess)
}

func (s *Service) sessionView(sess transcript.Session) (Session, error) {
	out := Session{Member: s.member(sess.Info, 0, 0), Dir: sess.Info.Dir, Usage: sess.Info.Usage, Earlier: sess.Omitted, Newer: sess.Newer}
	if len(sess.Turns) > 0 {
		out.Cursor = strconv.Itoa(sess.Turns[0].N)
	}
	out.Address = sess.Info.Address()
	book := s.avatars(sess.Info.RoomID)
	out.Member.Emoji = book.byID[sess.Info.ID]
	jobs := s.sessionJobs(sess)
	byID := map[string]shells.Info{}
	for _, in := range jobs {
		byID[in.ID] = in
	}
	claimed := map[string]bool{}
	for _, t := range sess.Turns {
		for _, it := range t.Items {
			if it.Kind == transcript.ToolUse && it.Tool.JobOp == "run" && it.Tool.JobID != "" {
				claimed[it.Tool.JobID] = true
			}
		}
	}
	for _, t := range sess.Turns {
		// The loader caches these items, so link a copy. A call's guess may take
		// only a job that no other call has, and that started during its turn.
		items := slices.Clone(t.Items)
		transcript.LinkJobs(items, slices.DeleteFunc(slices.Clone(jobs), func(in shells.Info) bool {
			return claimed[in.ID] || in.Started.Before(t.Started) || !t.Ended.IsZero() && in.Started.After(t.Ended)
		}))
		for _, it := range items {
			if it.Kind == transcript.ToolUse && it.Tool.JobOp == "run" && it.Tool.JobID != "" {
				claimed[it.Tool.JobID] = true
			}
		}
		turn := Turn{Harness: t.Harness, OutputDialect: t.OutputDialect, UsageNormalization: t.UsageNormalization, Profile: t.Profile, Model: t.Model, HarnessSession: t.HarnessSession, N: t.N, Status: t.Status, Started: t.Started, Ended: t.Ended, Usage: t.Usage, Omitted: t.Omitted}
		for _, it := range items {
			item := sessionItem(it)
			if in, ok := byID[item.JobID]; ok && item.JobOp == "run" {
				job := terminalsvc.ShellJobBlock(s.shellStore(), in)
				item.Job = &job
			}
			if it.Kind == transcript.Reply {
				if text, ok := serviceArtifactText(sess.Info.Meta, it.Text); ok {
					item.Text = text
				}
			}
			turn.Items = append(turn.Items, item)
		}
		out.Turns = append(out.Turns, turn)
	}
	rec, err := s.rooms().Resolve(sess.Info.RoomID)
	if err != nil {
		return Session{}, err
	}
	posts, media, err := s.transcriptMessages(rec, sess.Info, sess.Since, sess.Before)
	if err != nil {
		return Session{}, err
	}
	out.Posts, err = s.sessionPosts(rec, posts, sess.Info.ID, out.Address, sess.Since, sess.Before, book)
	if err != nil {
		return Session{}, err
	}
	out.Media, err = s.sessionMedia(rec, media, sess.Info.ID, out.Address, book)
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

func (s *Service) shellStore() *shells.Store { return terminalsvc.ShellStore(s.sessions().Root) }

// sessionJobs lists the session's shell jobs, if any of its calls used them.
func (s *Service) sessionJobs(sess transcript.Session) []shells.Info {
	used := false
	for _, t := range sess.Turns {
		for _, it := range t.Items {
			used = used || it.Kind == transcript.ToolUse && it.Tool.JobOp == "run"
		}
	}
	if !used {
		return nil
	}
	jobs, err := s.shellStore().ListOwner(sess.Info.ID)
	if err != nil {
		return nil
	}
	return jobs
}

// sessionMedia includes attachments from the entire conversation, including owner posts.
func (s *Service) sessionMedia(rec room.Record, msgs []room.Message, id, address string, book avatarBook) ([]Post, error) {
	return s.mediaWhere(rec, msgs, book, func(m room.Message) bool {
		return sessionParticipant(m, id, address)
	})
}

func sessionParticipant(m room.Message, id, address string) bool {
	return m.SentBySession(id, address) || m.SessionAddressed(id, address)
}

// mediaWhere is the attachments of the messages `in` selects, newest first.
func (s *Service) mediaWhere(rec room.Record, msgs []room.Message, book avatarBook, in func(room.Message) bool) ([]Post, error) {
	selected := make([]room.Message, 0)
	for _, m := range msgs {
		if len(m.Attachments) > 0 && in(m) {
			selected = append(selected, m)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].TS < selected[j].TS })
	if len(selected) > maxSessionMedia {
		selected = selected[len(selected)-maxSessionMedia:]
	}
	feed, err := s.feedOf(rec, selected, 0, book, nil)
	if err != nil {
		return nil, err
	}
	posts := feed.Posts
	if posts == nil {
		return []Post{}, nil
	}
	for i, j := 0, len(posts)-1; i < j; i, j = i+1, j-1 {
		posts[i], posts[j] = posts[j], posts[i]
	}
	return posts, nil
}

// sessionPosts selects the session's conversation before applying feed shaping.
func (s *Service) sessionPosts(rec room.Record, msgs []room.Message, id, address string, since, before time.Time, book avatarBook) ([]Post, error) {
	selected := make([]room.Message, 0)
	for _, m := range msgs {
		m = s.legacyQuestionPost(rec, m)
		if (!since.IsZero() && time.UnixMilli(m.TS).Before(since)) || (!before.IsZero() && time.UnixMilli(m.TS).After(before)) {
			continue
		}
		if sessionPost(rec, m, id, address) {
			selected = append(selected, m)
		}
	}
	return s.shapeSessionPosts(rec, selected, book)
}

// sessionPost is the shared conversation filter for indexed and supplied posts.
func sessionPost(rec room.Record, m room.Message, id, address string) bool {
	own := m.SentBySession(id, address)
	if m.Kind == "pr" || own && IsCompletion(m) {
		return false
	}
	return own || m.SessionAddressed(id, address) && (m.From != rec.Name || len(m.Attachments) > 0)
}

const (
	maxSessionPosts         = 300
	maxSessionMedia         = 200
	maxTranscriptSelections = 64
)

type sessionMessages struct {
	roomID, roomName, address string
	questionRevision          string
	posts, media              room.MessageSelection
}

// transcriptMessages reuses the room index and only revisits appended records.
// Page bounds apply to posts; media still covers the whole conversation.
func (s *Service) transcriptMessages(rec room.Record, info subagent.Info, since, before time.Time) ([]room.Message, []room.Message, error) {
	s.conversationMu.Lock()
	defer s.conversationMu.Unlock()
	if s.conversations == nil {
		s.conversations = map[string]*sessionMessages{}
	}
	address := info.Address()
	questionRevision, revisionErr := s.Notify.QuestionStore().Revision()
	selected := s.conversations[info.ID]
	if selected == nil || selected.roomID != rec.ID || selected.roomName != rec.Name || selected.address != address || selected.questionRevision != questionRevision || revisionErr != nil {
		if selected == nil && len(s.conversations) >= maxTranscriptSelections {
			for id := range s.conversations {
				delete(s.conversations, id)
				break
			}
		}
		selected = &sessionMessages{roomID: rec.ID, roomName: rec.Name, address: address, questionRevision: questionRevision}
		s.conversations[info.ID] = selected
	}
	posts, err := s.rooms().SelectedMessages(rec.ID, &selected.posts, func(m room.Message) bool {
		return sessionPost(rec, s.legacyQuestionPost(rec, m), info.ID, address)
	}, since, before, maxSessionPosts)
	if err != nil {
		return nil, nil, err
	}
	media, err := s.rooms().LatestSelectedMessages(rec.ID, &selected.media, func(m room.Message) bool {
		return len(m.Attachments) > 0 && sessionParticipant(m, info.ID, address)
	}, maxSessionMedia)
	return posts, media, err
}

// shapeSessionPosts keeps the newest 300 selected messages and shapes them as feed posts.
func (s *Service) shapeSessionPosts(rec room.Record, selected []room.Message, book avatarBook) ([]Post, error) {
	if len(selected) > maxSessionPosts {
		selected = selected[len(selected)-maxSessionPosts:]
	}
	feed, err := s.feedOf(rec, selected, 0, book, nil)
	if err != nil {
		return nil, err
	}
	if feed.Posts == nil {
		return []Post{}, nil
	}
	return feed.Posts, nil
}

// rootConversation selects the posts of the root seat. "@root" resolves to the room's own
// address, so agent and keeper posts to root are addressed to it, and an external root posts as
// the room (without Human). The internal root's own address is included for its earlier posts.
// Posts the human owner sent to other agents are not part of it.
func rootConversation(roomName string) func(room.Message) bool {
	internal := roomName + ":" + room.RootAgent
	return func(m room.Message) bool {
		if m.Kind == "pr" {
			return false
		}
		return m.Addressed(roomName) || m.Addressed(internal) || m.From == internal || m.From == roomName && !m.Human
	}
}

// RootSession is the root conversation shaped like any session view. With
// the internal root running it is that session's transcript. Otherwise
// (an external orchestrator holds the seat, or the internal root has not
// started) it has the seat holder and the conversation's posts but no turns:
// owner and keeper posts to root, root's posts, and what agents say to root.
func (s *Service) RootSession(roomRef string) (Session, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return Session{}, err
	}
	if !rec.Seat.External {
		members, err := s.Members(rec.ID, false)
		if err != nil {
			return Session{}, err
		}
		for _, m := range members {
			if m.Role == subagent.RoleRoot && !m.Closed && m.ID != "" {
				return s.TranscriptPage(m.ID, "", 6)
			}
		}
	}
	addr := rec.Name + ":" + room.RootAgent
	msgs, err := s.rooms().Messages(rec.ID)
	if err != nil {
		return Session{}, err
	}
	inRoot := rootConversation(rec.Name)
	var selected []room.Message
	for _, m := range msgs {
		if inRoot(m) {
			selected = append(selected, m)
		}
	}
	book := s.avatars(rec.ID)
	posts, err := s.shapeSessionPosts(rec, selected, book)
	if err != nil {
		return Session{}, err
	}
	media, err := s.mediaWhere(rec, msgs, book, inRoot)
	if err != nil {
		return Session{}, err
	}
	seat := rec.Seat
	member := Member{Role: subagent.RoleRoot, Name: room.RootAgent, Address: addr, Status: "seat", State: StateIdle, Hue: NameHue(rec.Name), Seat: &seat}
	return Session{Member: member, Address: addr, Posts: posts, Media: media, Dir: rec.Repo, Turns: []Turn{}}, nil
}

// Event is one live change. Kind is "posts" (new posts in RoomID), "rooms"
// (the room list changed), "members" (session metadata or status changed),
// "session" (one transcript or queue changed), "runs"
// (a workflow run in RoomID started, changed status, or ended), or "pins"
// (the pin list of RoomID changed). "prs" means tracked PRs or settings
// in RoomID changed, including changes made by the host watcher.
type Event struct {
	// WorkerWake marks changed worker inputs. Progress-only writes still reach
	// the UI, but must not trigger the worker that wrote them.
	WorkerWake bool               `json:"-"`
	Kind       string             `json:"kind"`
	RoomID     string             `json:"roomId,omitempty"`
	Posts      []Post             `json:"posts,omitempty"`
	Offset     int64              `json:"offset,omitempty"`
	Change     *listchange.Change `json:"change,omitempty"`
}

// Watch polls the stores every interval until ctx ends and calls emit for
// each change. Polling keeps it independent of inotify quirks and matches
// how room logs are appended (one locked write per post).
func (s *Service) Watch(ctx context.Context, interval time.Duration, emit func(Event)) {
	offsets := map[string]int64{}
	roomSig := ""
	memberSig := newMemberWatch()
	memberSig.runners = s.newRunners()
	defer memberSig.runners.close()
	scanCoordinators := func(hints map[string]bool) {
		if err := memberSig.runners.followCoordinators(ctx, hints); err != nil && ctx.Err() == nil {
			log.Printf("workflow runner watch: %v", err)
		}
	}
	scanCoordinators(map[string]bool{"": true})
	runSig := map[string]string{}
	jobs := newShellJobWatch()
	defer jobs.close()
	pinSig := map[string]string{}
	questionSig := map[string]string{}
	callRequestSig := map[string]string{}
	var callRequestVersion int64
	var questionVersion string
	prSig, prVersion := map[string]string{}, ""
	prWork := map[string]string{}
	goalVersion, noticeVersion := "", ""
	goalWork := map[string]string{}
	goalRows := map[string]string{}
	hookVersion := ""
	syncVersion := ""
	first := true
	liveEmit := func(ev Event) {
		if ev.Kind == "members" || ev.Kind == "rooms" {
			prVersion = ""
		}
		emit(ev)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for n := 0; ; n++ {
		s.scanSync(&syncVersion, first, emit)
		s.scanHookSpool(&hookVersion, first, emit)
		s.scan(offsets, &roomSig, memberSig, pinSig, first, liveEmit)
		s.scanQuestions(questionSig, &questionVersion, first, emit)
		s.scanCallRequests(callRequestSig, &callRequestVersion, first, emit)
		s.scanNotices(&noticeVersion, first, emit)
		// Runs change slowly and a store can hold thousands; scan them on
		// every fourth tick only.
		if n%RunScanEvery == 0 {
			s.scanRuns(runSig, first, emit)
			s.scanShellJobs(jobs, first, emit)
			// TODO: legacy/shim remove later — catches PR state that a dyna git pr CLI process wrote (track, verdict, hold, release, transfer, reviewer, policy); remove when item 2 moves those writes into the engine, whose writes wake Watch through PRsChanged.
			s.scanPRs(prSig, prWork, &prVersion, first, emit)
			s.scanGoals(goalRows, goalWork, &goalVersion, first, emit)
		}
		first = false
		if s.AfterFirstScan != nil {
			s.AfterFirstScan()
		}
		if !s.waitTick(ctx, tick.C, func() { s.scanPRs(prSig, prWork, &prVersion, false, emit) }, func() { s.scanDirtyMembers(memberSig, liveEmit) }, func() { scanCoordinators(s.takeCoordinatorHints()) }, func() { s.scanCallRequests(callRequestSig, &callRequestVersion, false, emit) }) {
			return
		}
	}
}

// waitTick waits for the next tick. Local state wakes scan only the affected
// store between ticks. It
// returns false when ctx ends.
func (s *Service) waitTick(ctx context.Context, tick <-chan time.Time, scanPRs, scanSessions, scanCoordinators, scanCallRequests func()) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-tick:
			return true
		case <-s.coordinatorWake:
			scanCoordinators()
		case <-s.callRequestWake:
			scanCallRequests()
		case <-s.prWake:
			scanPRs()
		case <-s.sessionWake:
			scanSessions()
		}
	}
}

func (s *Service) scan(offsets map[string]int64, roomSig *string, memberSig *memberWatch, pinSig map[string]string, first bool, emit func(Event)) {
	store := s.rooms()
	records, err := store.List(true)
	if err != nil {
		return
	}
	sig := ""
	for _, rec := range records {
		s.scanIssues(rec.ID, pinSig, first, emit)
		pauseKey := "pause:" + rec.ID
		revision := strconv.FormatUint(rec.SessionPause.Revision, 10)
		if old, ok := pinSig[pauseKey]; ok && old != revision && !first {
			after, _ := strconv.ParseUint(old, 10, 64)
			if events, e := store.SessionPauseEvents(rec.ID, after); e == nil {
				for _, event := range events {
					emit(Event{Kind: event.Kind, RoomID: rec.ID})
				}
			}
		}
		pinSig[pauseKey] = revision
		sig += revision + ";"
		sig += fmt.Sprintf("%s/%s/%s/%v/%v/%d/%s;", rec.ID, rec.Name, rec.DisplayName, rec.ManualArchive, rec.Pinned, rec.PinOrder, rec.LastActivity)
		if sig := pinsSig(store, rec.ID); sig != pinSig[rec.ID] {
			pinSig[rec.ID] = sig
			if !first {
				emit(Event{Kind: "pins", RoomID: rec.ID})
			}
		}
		size, err := store.FileSize(rec.ID)
		if err != nil {
			continue
		}
		if first {
			offsets[rec.ID] = size
			continue
		}
		prev := offsets[rec.ID] // a room created after the first scan starts at 0
		if size == prev {
			continue
		}
		posts, next, err := s.FeedSince(rec.ID, prev)
		if err != nil {
			continue
		}
		offsets[rec.ID] = next
		if len(posts) > 0 {
			emit(Event{Kind: "posts", RoomID: rec.ID, Posts: posts, Offset: next})
		}
	}
	if s.membersChanged(memberSig, records, first) {
		if sessions, err := memberSig.list(s); err == nil {
			s.scanMembers(sessions, records, memberSig, nil, first, emit)
			memberSig.runners.follow(sessions)
		}
	}
	if !first && sig != *roomSig {
		emit(Event{Kind: "rooms"})
	}
	*roomSig = sig
}

// RunScanEvery is how many Watch ticks pass between run scans.
const RunScanEvery = 4

// MemberFullScanEvery is how many member scans pass between full member
// scans. Other scans keep closed sessions' events, so an edit of a closed
// session appears within 10 seconds at the 500 ms Watch interval.
const MemberFullScanEvery = 20

func (s *Service) scanRuns(sigs map[string]string, first bool, emit func(Event)) {
	next := s.runSigs()
	for id, v := range next {
		if !first && sigs[id] != v {
			emit(Event{Kind: "runs", RoomID: id})
		}
		sigs[id] = v
	}
	for id := range sigs {
		if _, ok := next[id]; !ok {
			delete(sigs, id)
			emit(Event{Kind: "runs", RoomID: id})
		}
	}
}

// pinsSig is a cheap change marker for a room's pins. It is empty when the
// room's pins never changed.
func pinsSig(store *room.Store, roomID string) string {
	rev, err := store.PinsRevision(roomID)
	if err != nil || rev == 0 {
		return ""
	}
	return strconv.FormatInt(rev, 10)
}

// scanPRs adapts PR changes to the room event stream.
func (s *Service) scanPRs(signatures, work map[string]string, version *string, first bool, emit func(Event)) {
	s.PRs.Scan(signatures, work, version, first, func(kind, roomID string, change *listchange.Change, workerWake bool) {
		emit(Event{Kind: kind, RoomID: roomID, Change: change, WorkerWake: workerWake})
	})
}
