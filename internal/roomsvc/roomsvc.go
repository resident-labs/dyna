// Package roomsvc is the room behavior that every Dyna front end shares:
// room lists, feeds, member trees, posting, live watching, and read-only
// session transcripts, starting and stopping sessions, and workflow runs. It
// returns plain data and draws nothing. The desktop client binds it directly,
// and the web app serves the same calls over HTTP.
package roomsvc

import (
	"context"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/listpage"
	"dyna-agent/internal/notify"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/transcript"
	"dyna-agent/internal/workeroutput"
)

// Feed collapse rule: a body of at most
// CollapseAtLines lines shows in full. A longer one shows its first
// paragraph, capped at FoldedLines lines.
const (
	CollapseAtLines = 10
	FoldedLines     = 6
	// FoldWidth is the column width the rule measures wrapped lines at. It
	// matches a comfortable feed width, so the fold is stable across window
	// sizes and front ends.
	FoldWidth = 96
)

// Service reads and writes rooms through the same stores and delivery path
// as the dyna CLI.
type Service struct {
	Notify *notify.Service
	// PRs supplies PR views used by room events and service composition.
	PRs *prview.Service

	mu           sync.Mutex
	issueStartMu sync.Mutex
	loader       *transcript.Loader
	runIdx       runIndex
	// results caches full completion texts by post ID. A finished turn's
	// result never changes. The LRU keeps the newest 256 entries, so the
	// long-lived web server does not retain every completion forever.
	results  *resultCache
	ownerMu  sync.Mutex
	ownerIDs map[string]*ownerPostIDs
	snap     sessionSnapshot
	// prWake asks Watch to scan PRs now, after the engine's own PR state
	// write. It holds one pending wake.
	prWake      chan struct{}
	storeWake   chan struct{}
	storeMu     sync.Mutex
	storeTopics map[string]bool
	postRooms   map[string]bool
	// callRequestWake asks Watch to read committed local call-request changes.
	callRequestWake chan struct{}
	// RunnerExited, when set, runs after a watched turn runner exits, with
	// its session ID: the engine applies what the runner spooled.
	RunnerExited func(session string)
	// AfterFirstScan, when set, runs once after Watch's first scan pass,
	// after every room's post offset baseline is established. Watch sets
	// that baseline without emitting the posts it covers, so a reader that
	// wants every post from before the engine started must read the stores
	// only after this runs.
	AfterFirstScan func()
	// CoordinatorExited recovers a registered workflow coordinator's spool.
	CoordinatorExited func(run string)
	coordinatorWake   chan struct{}
	coordinatorMu     sync.Mutex
	coordinatorHints  map[string]bool
	// sessionWake asks Watch to scan the sessions in dirty now. It holds one
	// pending wake, so a burst of session writes gives one scan. Watch takes
	// dirty, so it assumes one Watch per Service: engine/lifecycle.go starts
	// the only one. A second Watch would take the other's sessions.
	sessionWake chan struct{}
	dirtyMu     sync.Mutex
	dirty       map[string]bool
	dirtyAll    bool
	// onMemberScan, for tests, sees each members scan: the sessions it read
	// again, or -1 for all of them.
	onMemberScan func(sessions int)
}

type ownerPostIDs struct {
	name      string
	selection room.MessageSelection
}

// PRsChanged tells Watch that a PR state write committed, so it scans PRs at
// once instead of at its next tick. It never blocks.
func (s *Service) PRsChanged() {
	select {
	case s.prWake <- struct{}{}:
	default:
	}
}

// StoreChanged queues a committed store topic for the sole room watcher.
// A burst keeps one wake and one scan per topic.
func (s *Service) StoreChanged(topic string) {
	s.storeMu.Lock()
	if s.storeTopics == nil {
		s.storeTopics = map[string]bool{}
	}
	s.storeTopics[topic] = true
	s.storeMu.Unlock()
	select {
	case s.storeWake <- struct{}{}:
	default:
	}
}

// PostsChanged queues each room independently of other store topics.
func (s *Service) PostsChanged(roomID string) {
	s.storeMu.Lock()
	if s.postRooms == nil {
		s.postRooms = map[string]bool{}
	}
	s.postRooms[roomID] = true
	s.storeMu.Unlock()
	select {
	case s.storeWake <- struct{}{}:
	default:
	}
}

func (s *Service) scanChangedPosts(offsets map[string]int64, emit func(Event)) {
	s.storeMu.Lock()
	rooms := s.postRooms
	s.postRooms = nil
	s.storeMu.Unlock()
	for id := range rooms {
		s.scanPosts(id, offsets, false, emit)
	}
}

func (s *Service) takeStoreTopics() map[string]bool {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	topics := s.storeTopics
	s.storeTopics = nil
	return topics
}

// CallRequestsChanged wakes the sole frontend publisher after a local commit.
// Other engines detect that commit through the SQLite revision on their tick.
func (s *Service) CallRequestsChanged() {
	select {
	case s.callRequestWake <- struct{}{}:
	default:
	}
}

// SessionChanged tells Watch that a write to session id committed, so it
// scans that session at once instead of every session. It never blocks.
func (s *Service) SessionChanged(id string) { s.markDirty(id) }

// SessionsChanged makes Watch scan every session at once, for a change that
// writes no row, such as a turn runner's exit. It never blocks.
func (s *Service) SessionsChanged() {
	if s.PRs != nil {
		s.PRs.SessionsChanged()
	}
	s.snap.exits.Add(1)
	s.markDirty("")
}

func (s *Service) markDirty(id string) {
	s.dirtyMu.Lock()
	if id == "" {
		s.dirtyAll = true
	} else {
		if s.dirty == nil {
			s.dirty = map[string]bool{}
		}
		s.dirty[id] = true
	}
	s.dirtyMu.Unlock()
	select {
	case s.sessionWake <- struct{}{}:
	default:
	}
}

// takeDirty returns the sessions named since the last call, or all.
func (s *Service) takeDirty() (map[string]bool, bool) {
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	ids, all := s.dirty, s.dirtyAll
	s.dirty, s.dirtyAll = nil, false
	return ids, all
}

// New wraps a notify service. Nil uses the default stores.
func New(n *notify.Service) *Service {
	if n == nil {
		n = notify.Default()
	}
	s := &Service{Notify: n, results: newResultCache(), prWake: make(chan struct{}, 1), storeWake: make(chan struct{}, 1), callRequestWake: make(chan struct{}, 1), sessionWake: make(chan struct{}, 1), coordinatorWake: make(chan struct{}, 1)}
	s.PRs = &prview.Service{Notify: n, SessionInRoom: s.SessionInRoom, WorkspaceRoot: s.files().WorkspaceRoot}
	n.Sessions.ClosePRs = func(ctx context.Context, from, heir subagent.Info) ([]sessionclose.Outcome, error) {
		return s.PRs.Watcher().CloseSession(ctx, from, heir)
	}
	n.Sessions.HandOffTerminalRequests = n.RepostTerminalRequests
	return s
}

func (s *Service) rooms() *room.Store        { return s.Notify.Rooms }
func (s *Service) sessions() *subagent.Store { return s.Notify.Sessions.Store }

// Room is one row of the room rail.
type Room struct {
	DisplayName  string    `json:"displayName,omitempty"`
	Pinned       bool      `json:"pinned"`
	PinOrder     int       `json:"pinOrder"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Repo         string    `json:"repo,omitempty"`
	RootProfile  string    `json:"rootProfile,omitempty"`
	LastActivity time.Time `json:"lastActivity"`
	Unread       int       `json:"unread"`
	Running      int       `json:"running"`
	Members      int       `json:"members"`
	Archived     bool      `json:"archived"`
	Paused       bool      `json:"paused,omitempty"` // the owner paused this room's sessions
}

// Rooms lists every room, active first, then by recent activity.
func (s *Service) Rooms() ([]Room, error) {
	store := s.rooms()
	records, err := store.List(true)
	if err != nil {
		return nil, err
	}
	counts, err := s.sessions().RoomMemberCounts()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if store.Now != nil {
		now = store.Now()
	}
	live := s.Notify.ActiveRoomIDs()
	var out []Room
	for _, rec := range records {
		r := Room{DisplayName: rec.DisplayName, Pinned: rec.Pinned, PinOrder: rec.PinOrder, ID: rec.ID, Name: rec.Name, Repo: rec.Repo, RootProfile: rec.RootProfile, LastActivity: rec.LastActivity, Archived: rec.Archived(now, live[rec.ID]), Paused: rec.SessionPause.Paused}
		ids, err := s.ownerPosts(rec)
		if err != nil {
			return nil, err
		}
		reads, err := store.OwnerReadIDs(rec.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !reads[id] {
				r.Unread++
			}
		}
		r.Members, r.Running = counts[rec.ID].Members, counts[rec.ID].Running
		out = append(out, r)
	}
	// Rail order is stable, so a room never moves under the pointer: pinned
	// rooms in pin order, then active rooms by name, then archived rooms by
	// name. Unread and running state show as badges, not as order.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Pinned && a.PinOrder != b.PinOrder {
			return a.PinOrder < b.PinOrder
		}
		if a.Archived != b.Archived {
			return b.Archived
		}
		return a.Name < b.Name
	})
	return out, nil
}

// ownerPosts returns the IDs of the posts unread in the room rail: the room's
// own posts addressed to the owner. A post written in a DM or in the owner's
// voice call is not in the room chat, so it is not unread in the room either.
func (s *Service) ownerPosts(rec room.Record) ([]string, error) {
	ids, err := s.ownerSelection(rec)
	if err != nil {
		return nil, err
	}
	elsewhere, err := s.rooms().OtherHomes(rec.ID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, other := elsewhere[id]; !other {
			out = append(out, id)
		}
	}
	return out, nil
}

// ownerSelection returns the IDs of the posts addressed to the owner, kept in
// one selection that grows with the room's log.
func (s *Service) ownerSelection(rec room.Record) ([]string, error) {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if s.ownerIDs == nil {
		s.ownerIDs = map[string]*ownerPostIDs{}
	}
	selected := s.ownerIDs[rec.ID]
	if selected == nil || selected.name != rec.Name {
		selected = &ownerPostIDs{name: rec.Name}
		s.ownerIDs[rec.ID] = selected
	}
	return s.rooms().MessageIDs(rec.ID, &selected.selection, func(message room.Message) bool {
		return countsUnread(message, rec.Name)
	})
}

// NameHues is how many stable name colors front ends define. Gold and
// coral are reserved for live and error state, so no name uses them.
const NameHues = 8

// NameHue hashes an address to its stable name color index, so a sender
// has one color in every view and every client.
func NameHue(addr string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(addr))
	return int(h.Sum32() % NameHues)
}

// ForOwner reports a post addressed to the room owner by someone else.
func ForOwner(m room.Message, roomName string) bool {
	return m.Addressed(roomName) && m.From != roomName
}

// countsUnread reports whether a post to the owner can be unread. Shell exit
// records live in shell jobs, and voice call lines are heard live in the call
// and shown only in Voice's call widget, so neither is ever unread. A quiet
// engine notice (CI, PR and service events) is informational and never counts.
func countsUnread(m room.Message, roomName string) bool {
	if !m.Deliverable() {
		return false
	}
	return ForOwner(m, roomName) && !IsShellResult(m) && m.Source != room.SourceVoice
}

// SpeakerLabel names a post's sender in a feed: "you" for a post the human
// owner typed, "root" for other owner posts (the external orchestrator), and
// the member's name inside its own room otherwise. A typed turn event names
// the session whose turn ended, which the engine posted on its behalf.
func SpeakerLabel(m room.Message, roomName string) string {
	if ev, ok := m.TurnResult(); ok {
		return strings.TrimPrefix(ev.Session, roomName+":")
	}
	if m.From == room.EngineSender && m.EngineEvent() {
		return room.EngineSender
	}
	if m.From == roomName {
		return room.OwnerLabel(m.Human)
	}
	return strings.TrimPrefix(m.From, roomName+":")
}

// IsCompletion reports an automatic result record: the typed end of a turn,
// the typed result of a detached shell job or workflow run, or a completion
// stored before those events. The owner feed folds a result like a completion
// and needs no separate flag.
func IsCompletion(m room.Message) bool {
	if _, ok := m.TurnResult(); ok {
		return true
	}
	if _, ok := m.Shell(); ok {
		return true
	}
	if _, ok := m.RunResult(); ok {
		return true
	}
	// TODO: legacy/shim remove later — a completion stored before the typed
	// turn events carries its state as the post kind. Remove when retained room
	// history ages out.
	return m.Kind == "completed" || m.Kind == "failed" || m.Kind == "stopped"
}

// IsShellResult reports the automatic exit record of a `dyna shell` job.
// The record still wakes the job's parent. Front ends show it in shell-jobs,
// so it never counts as unread for the owner.
func IsShellResult(m room.Message) bool {
	if _, ok := m.Shell(); ok {
		return true
	}
	// TODO: legacy/shim remove later — a shell result stored before the typed
	// shell events names its job in the sender. Remove when retained room
	// history ages out.
	return IsCompletion(m) && strings.HasPrefix(m.From, "shell:")
}

// Post is one feed entry with the facts a front end needs to draw it.
type Post struct {
	room.Message
	// Detail opens the UI view for this completion without a shell command.
	Detail *PostDetail `json:"detail,omitempty"`
	// Owner marks a post the room owner wrote; Human, from the message, marks
	// one a person typed. Owner posts never fold.
	Owner bool `json:"owner"`
	// Completion is a session's turn-end record ("completed", "failed",
	// "stopped"). Completions fold to their first paragraph.
	Completion bool `json:"completion"`
	// Preview is the folded body when the collapse rule folds this post.
	// Empty means show Text in full.
	Preview string `json:"preview,omitempty"`
	// Hidden is the number of wrapped lines the preview leaves out.
	Hidden int  `json:"hidden,omitempty"`
	Unread bool `json:"unread,omitempty"`
	// FromHue is the sender's stable name color index (see NameHue).
	FromHue int `json:"fromHue"`
	// FromEmoji is the sending session's avatar; empty for the owner.
	FromEmoji string `json:"fromEmoji,omitempty"`
	// Speaker is the sender's label (see SpeakerLabel).
	Speaker string `json:"speaker"`
	// FromRoom names the room the owner wrote this post from, set only on a
	// post the owner sent from another room. The feed marks such a post as a
	// cross-room message.
	FromRoom string `json:"fromRoom,omitempty"`
	// FullResult marks a completion whose Text the feed replaced with the
	// turn's full result from the session store. The stored post keeps its
	// short preview; agents read that one.
	FullResult bool `json:"fullResult,omitempty"`
	// ResultCut is set when the full result passed MaxFeedResultKB.
	ResultCut bool `json:"resultCut,omitempty"`
	// Pinned marks a post in the room's pin list. PinnedAt (unix ms) and
	// PinnedBy (an address) are set only on posts that Pins returns.
	// Views lists the feed views the post shows in, the sender's home first.
	// Only room feed pages and live posts set it. Channels repeats Views for a
	// browser tab still running the bundle from before the feed view rename.
	Views    []string `json:"views,omitempty"`
	Channels []string `json:"channels,omitempty"`
	// ForRoot marks a post addressed to the room's root session. The owner
	// reads it in the room chat as a post to them, so it gets the gold mark.
	ForRoot bool `json:"forRoot,omitempty"`
	// Reply names who a reply to this post answers, read from the post's stored
	// sender identity rather than from the shape of its address text.
	Reply    *ReplyTarget `json:"reply,omitempty"`
	Pinned   bool         `json:"pinned,omitempty"`
	PinnedAt int64        `json:"pinnedAt,omitempty"`
	PinnedBy string       `json:"pinnedBy,omitempty"`
}

// setViews records the feed views a post shows in, and repeats them under the
// name an open tab still reads.
//
// TODO: legacy/shim remove later — stop sending Channels when a page reset
// reloads every open tab onto the new bundle.
func (p *Post) setViews(views []string) {
	p.Views, p.Channels = views, views
}

// PostDetail identifies the stored job, workflow, or session turn a post reports.
type PostDetail struct {
	Kind string `json:"kind"` // shell, run, or session
	ID   string `json:"id"`
	Turn int    `json:"turn,omitempty"`
}

// A reply target names one recipient of a reply: a session that wrote the
// post, or the room's own address, which a session reaches as @root.
const (
	ReplySession = "session"
	ReplyRoom    = "room"
)

// ReplyTarget is who a reply to a post answers. The composer routes the reply
// by this target, so no address prefix decides a recipient.
type ReplyTarget struct {
	Kind string `json:"kind"` // ReplySession or ReplyRoom
	// ID is the session bound to the sender address, and empty for the room.
	ID string `json:"id,omitempty"`
	// Address is the address a reply names: the sender's own address today, or
	// the room's. It is empty when the stored session is gone or its address
	// now belongs to another session, so the reply names nobody.
	Address string `json:"address,omitempty"`
}

// replyTarget reads who a reply to m answers from the stored sender identity.
// A session's own post names the session the sender address is bound to, at the
// address that session answers at now, which survives a rename and works
// across rooms. The room's owner and its root seat name the room, which a
// composer writes as @root, so the seat's holder can change. A post the owner
// typed has no target: a reply to it keeps the draft's recipients.
func replyTarget(m room.Message, roomName string, book avatarBook) *ReplyTarget {
	if m.From == roomName || m.From == roomName+":"+room.RootAgent {
		if m.Human {
			return nil
		}
		return &ReplyTarget{Kind: ReplyRoom, Address: roomName}
	}
	if id := m.AuthorSessionID(); id != "" {
		// The stored address may be out of date, or now belong to another
		// session, so the target names the session only when it still holds it.
		cur, ok := book.address(id)
		if !ok {
			return &ReplyTarget{Kind: ReplySession, ID: id}
		}
		return &ReplyTarget{Kind: ReplySession, ID: id, Address: cur}
	}
	// An engine, script or worker post has no session to answer, so a reply
	// goes to the room's root, as it does for the owner's own posts.
	return &ReplyTarget{Kind: ReplyRoom, Address: roomName}
}

// Feed is a room's posts in append order plus the log offset to watch from.
type Feed struct {
	Room   Room   `json:"room"`
	Posts  []Post `json:"posts"`
	Offset int64  `json:"offset"`
	// Earlier counts posts before this page. Zero means the page reached
	// the start of the retained log.
	Earlier     int            `json:"earlier,omitempty"`
	Newer       int            `json:"newer,omitempty"`
	Cursor      string         `json:"cursor,omitempty"`
	Unread      int            `json:"unread"`
	FirstUnread string         `json:"firstUnread,omitempty"`
	ViewUnread  map[string]int `json:"viewUnread"`
	// TODO: legacy/shim remove later — ChannelUnread repeats ViewUnread for a
	// browser tab still running the bundle from before the feed view rename,
	// whose sidebar and voice transcript would otherwise lose their counts.
	ChannelUnread map[string]int `json:"channelUnread"`
}

// FeedTail returns a bounded page in append order. An empty cursor selects
// the newest posts; before selects posts older than that post ID.
func (s *Service) FeedTail(ref, before string, keep int) (Feed, error) {
	return s.feedPage(ref, before, keep, s.rooms().ReadPage)
}

// FeedAfter returns posts newer than an exclusive post ID.
func (s *Service) FeedAfter(ref, after string, keep int) (Feed, error) {
	return s.feedPage(ref, after, keep, s.rooms().ReadAfter)
}

// FeedAround returns one bounded page containing a post, including distant pins.
func (s *Service) FeedAround(ref, id string, keep int) (Feed, error) {
	return s.feedPage(ref, id, keep, s.rooms().ReadAround)
}

// FeedSearch is a page of room posts that match a search, newest first.
type FeedSearch struct {
	Posts []Post `json:"posts"`
	// More reports older matches beyond this page.
	More bool `json:"more"`
}

// SearchFeed finds posts in the whole retained room log. The web feed holds
// only a window of the log, so it cannot search on its own.
func (s *Service) SearchFeed(ref, query string, limit int) (FeedSearch, error) {
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return FeedSearch{}, err
	}
	// A post written in a DM or in the owner's voice call is not in the room
	// chat, so a room search finds no result for it: its own conversation lists
	// it.
	elsewhere, err := s.rooms().OtherHomes(rec.ID)
	if err != nil {
		return FeedSearch{}, err
	}
	msgs, more, err := s.rooms().SearchMessages(rec.ID, query, limit, func(m room.Message) bool {
		return roomFeedPost(homeOf(elsewhere, m.ID))
	})
	if err != nil {
		return FeedSearch{}, err
	}
	all, rev := s.snapshot()
	// A result row needs only the stored text the search matched; the jump
	// loads the full post through FeedAround.
	posts, err := s.postsOf(rec, msgs, s.avatarBook(rec.ID, all, rev), nil, false)
	if posts == nil {
		posts = []Post{}
	}
	return FeedSearch{Posts: posts, More: more}, err
}

type feedPageReader func(string, string, int, room.PageFilter) (room.MessagePage, error)

func (s *Service) feedPage(ref, cursor string, keep int, read feedPageReader) (Feed, error) {
	keep = listpage.Limit(keep)
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return Feed{}, err
	}
	all, rev := s.snapshot()
	return s.roomFeed(rec, keep, cursor, s.avatarBook(rec.ID, all, rev), s.membersOf(rec, all, rev, true), read)
}

func (s *Service) roomFeed(rec room.Record, keep int, cursor string, book avatarBook, members []Member, read feedPageReader) (Feed, error) {
	store := s.rooms()
	reads, err := store.OwnerReadIDs(rec.ID)
	if err != nil {
		return Feed{}, err
	}
	elsewhere, err := store.OtherHomes(rec.ID)
	if err != nil {
		return Feed{}, err
	}
	views := newFeedViews(members)
	counts := map[string]int{}
	// The page selects and counts only the room's own posts, before it pages:
	// a run of DM posts never leaves a page empty or repeats a cursor.
	page, err := read(rec.ID, cursor, keep, room.PageFilter{
		Keep: func(m room.Message) bool {
			return roomFeedPost(homeOf(elsewhere, m.ID))
		},
		Unread: func(m room.Message) bool {
			unread := countsUnread(m, rec.Name) && !reads[m.ID]
			if unread {
				for _, view := range views.viewsFor(m, homeOf(elsewhere, m.ID)) {
					counts[view]++
				}
			}
			return unread
		},
	})
	if err != nil {
		return Feed{}, err
	}
	out, err := s.feedOf(rec, page.Messages, page.Offset, book, reads)
	for i := range out.Posts {
		out.Posts[i].setViews(views.viewsFor(out.Posts[i].Message, homeOf(elsewhere, out.Posts[i].ID)))
		out.Posts[i].ForRoot = views.addressedToRoot(out.Posts[i].Message)
	}
	out.Earlier, out.Newer, out.Cursor = page.Earlier, page.Newer, page.Cursor
	out.Unread, out.FirstUnread = page.Unread, page.FirstUnread
	out.ViewUnread = counts
	out.ChannelUnread = counts
	return out, err
}

// homeOf is the stored home of one post: the room feed, or the conversation the
// room's other homes name for it.
func homeOf(elsewhere map[string]room.Conversation, postID string) room.Conversation {
	if home, ok := elsewhere[postID]; ok {
		return home
	}
	return room.ConversationRoom
}

// roomFeedPost reports whether the room feed carries a post whose home is home.
// It lists the room's own posts and the owner's voice call, whose lines travel
// with the feed so the call view can replay a call; no feed view lists the call.
// A post written in a DM is not in the room feed: its own DM lists it.
func roomFeedPost(home room.Conversation) bool {
	return home == room.ConversationRoom || home == room.ConversationVoice
}

// feedOf decorates already selected room or session messages through one path.
func (s *Service) feedOf(rec room.Record, msgs []room.Message, offset int64, book avatarBook, reads map[string]bool) (Feed, error) {
	posts, err := s.postsOf(rec, msgs, book, reads, true)
	if err != nil {
		return Feed{}, err
	}
	return Feed{Room: Room{DisplayName: rec.DisplayName, Pinned: rec.Pinned, PinOrder: rec.PinOrder, ID: rec.ID, Name: rec.Name, Repo: rec.Repo, RootProfile: rec.RootProfile, LastActivity: rec.LastActivity}, Offset: offset, Posts: posts}, nil
}

// postsOf decorates messages as posts. full replaces a completion's stored
// text with its turn's full result, as the feed shows it; search keeps the
// stored text it matched.
func (s *Service) postsOf(rec room.Record, msgs []room.Message, book avatarBook, reads map[string]bool, full bool) ([]Post, error) {
	store := s.rooms()
	if reads == nil {
		var err error
		reads, err = store.OwnerReadIDs(rec.ID)
		if err != nil {
			return nil, err
		}
	}
	pins, err := store.Pins(rec.ID)
	if err != nil {
		return nil, err
	}
	pinned := map[string]bool{}
	for _, p := range pins {
		pinned[p.ID] = true
	}
	var out []Post
	for _, m := range msgs {
		post := decorate(m, rec.Name, reads, book)
		if full {
			post = s.expand(post, book)
		}
		post.Pinned = pinned[m.ID]
		out = append(out, post)
	}
	return out, nil
}

func decorate(m room.Message, roomName string, reads map[string]bool, book avatarBook) Post {
	name := m.From
	if ev, ok := m.TurnResult(); ok {
		// The engine posts a turn end on the session's behalf: its name colors
		// the row and finds the sender's avatar.
		name = ev.Session
	}
	p := Post{Message: m, Owner: m.From == roomName, Completion: IsCompletion(m), FromHue: NameHue(name), Speaker: SpeakerLabel(m, roomName), Reply: replyTarget(m, roomName, book)}
	if !p.Owner {
		p.FromEmoji = book.forPost(name, m.TS)
	}
	p.Unread = countsUnread(m, roomName) && !reads[m.ID]
	if !p.Owner {
		p.Preview, p.Hidden = Fold(m.Text, p.Completion)
	}
	return p
}

// Fold applies the feed collapse rule to a post body. It returns an empty
// preview when the body shows in full. A completion folds without the line
// minimum that an ordinary agent post has; both keep the first paragraph.
func Fold(text string, completion bool) (preview string, hidden int) {
	text = strings.TrimSpace(text)
	full := wrappedLines(text)
	if !completion && full <= CollapseAtLines {
		return "", 0
	}
	paras := strings.Split(text, "\n\n")
	if len(paras) > 1 {
		paras = paras[:1]
	}
	short := strings.Join(paras, "\n\n")
	if lines := strings.Split(short, "\n"); len(lines) > FoldedLines {
		short = strings.Join(lines[:FoldedLines], "\n")
	}
	short = capWrapped(short, FoldedLines)
	hidden = max(0, full-wrappedLines(short))
	if hidden == 0 {
		return "", 0
	}
	return short, hidden
}

// wrappedLines estimates the lines text takes at FoldWidth columns.
func wrappedLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		n += max(1, (len([]rune(line))+FoldWidth-1)/FoldWidth)
	}
	return n
}

// capWrapped trims text to at most n wrapped lines.
func capWrapped(text string, n int) string {
	var out []string
	used := 0
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		need := max(1, (len(r)+FoldWidth-1)/FoldWidth)
		if used+need > n {
			if room := n - used; room > 0 {
				out = append(out, strings.TrimSpace(string(r[:room*FoldWidth]))+"…")
			}
			break
		}
		out = append(out, line)
		used += need
	}
	return strings.Join(out, "\n")
}

// FeedSince returns posts appended after offset and the next offset.
func (s *Service) FeedSince(roomID string, offset int64) ([]Post, int64, error) {
	store := s.rooms()
	rec, err := store.Resolve(roomID)
	if err != nil {
		return nil, offset, err
	}
	msgs, next, err := store.ReadFrom(rec.ID, offset)
	if err != nil {
		return nil, offset, err
	}
	reads, err := store.OwnerReadIDs(rec.ID)
	if err != nil {
		return nil, offset, err
	}
	all, rev := s.snapshot()
	book := s.avatarBook(rec.ID, all, rev)
	views := newFeedViews(s.membersOf(rec, all, rev, true))
	elsewhere, err := store.OtherHomes(rec.ID)
	if err != nil {
		return nil, offset, err
	}
	var out []Post
	for _, m := range msgs {
		post := s.expand(decorate(m, rec.Name, reads, book), book)
		post.setViews(views.viewsFor(m, homeOf(elsewhere, m.ID)))
		post.ForRoot = views.addressedToRoot(m)
		out = append(out, post)
	}
	return out, next, nil
}

// MarkRead marks a post read by the owner.
func (s *Service) MarkRead(roomID, messageID string) error {
	return s.rooms().MarkOwnerRead(roomID, messageID)
}

// MarkReadMany marks posts read by the owner, in one write.
func (s *Service) MarkReadMany(roomID string, messageIDs []string) error {
	return s.rooms().MarkOwnerReadMany(roomID, messageIDs)
}

// MarkRoomRead marks every post that counts as unread in the room as read, so
// opening the room chat clears its badge, even for posts the feed has not loaded.
func (s *Service) MarkRoomRead(roomID string) error {
	rec, err := s.rooms().Resolve(roomID)
	if err != nil {
		return err
	}
	ids, err := s.ownerPosts(rec)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return s.rooms().MarkOwnerReadMany(rec.ID, ids)
}

// Member is one row of a room's member tree. The first row is the owner.
type Member struct {
	Service bool `json:"service,omitempty"`
	// TODO: legacy/shim remove later — keep the finished API flag for older desktop clients until they use status=dormant.
	Finished  bool      `json:"finished,omitempty"`
	Resumable bool      `json:"resumable"`
	Role      string    `json:"role,omitempty"`
	Model     string    `json:"model,omitempty"`
	Transfer  string    `json:"transfer,omitempty"`
	ID        string    `json:"id,omitempty"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Profile   string    `json:"profile,omitempty"`
	Parent    string    `json:"parent,omitempty"`
	Depth     int       `json:"depth"`
	Children  int       `json:"children"`
	Owner     bool      `json:"owner,omitempty"`
	Status    string    `json:"status"`
	State     string    `json:"state"`
	Turns     int       `json:"turns"`
	Created   time.Time `json:"created"`
	Activity  time.Time `json:"lastActivity"`
	// Ended is when the session went dormant or closed; zero while it is open.
	Ended time.Time `json:"ended,omitzero"`
	// Doing says what a live session does now: its running tool call or
	// compaction, and when it started (see workeroutput.LastActivity).
	Doing workeroutput.Activity `json:"doing,omitzero"`
	// Responsibilities counts, by kind, what stops an open session from
	// finishing on its own (subagent.Responsibility.Kind). Empty when the
	// session owns nothing, is closed, or its lists cannot be read.
	Responsibilities map[string]int         `json:"responsibilities,omitempty"`
	CloseSummary     []sessionclose.Outcome `json:"closeSummary,omitempty"`
	Closed           bool                   `json:"closed,omitempty"`
	Hue              int                    `json:"hue"`
	Emoji            string                 `json:"emoji,omitempty"` // avatar; empty for the owner
	// Seat is set on the owner entry: who holds the room's root seat.
	Seat *room.Seat `json:"seat,omitempty"`
	// OwnerDisplay says this session's own shells use the owner's display.
	// The app labels such a session.
	OwnerDisplay bool `json:"ownerDisplay,omitempty"`
}

// Member states, in the shared color language: live is gold, done is green,
// error is red, idle and closed are quiet.
const (
	StateLive   = "live"
	StateDone   = "done"
	StateError  = "error"
	StateIdle   = "idle"
	StateClosed = "closed"
)

// Members returns the room owner and the room's sessions as a tree in
// creation order. Closed sessions are included when withClosed. Each open
// session carries the count of what stops it from finishing on its own.
func (s *Service) Members(ref string, withClosed bool) ([]Member, error) {
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return nil, err
	}
	view, err := s.readRoster(s.sessions().Inspect, false)
	if err != nil {
		return nil, err
	}
	members := s.memberItems(view, rec, withClosed)
	s.countResponsibilities(members, view.all)
	return members, nil
}

// countResponsibilities fills each open session's responsibility counts from
// one read of every store, so a member list costs the same as one close
// check. The owner row, a closed session and a session whose lists cannot be
// read keep no count.
func (s *Service) countResponsibilities(members []Member, all []subagent.Info) {
	manager := s.Notify.Sessions
	if manager == nil {
		return
	}
	infos := make(map[string]subagent.Info, len(all))
	for _, info := range all {
		infos[info.ID] = info
	}
	owners := make([]subagent.Info, 0, len(members))
	rows := make([]int, 0, len(members))
	for i, m := range members {
		info, ok := infos[m.ID]
		if !ok || m.Owner || m.Closed || info.Status == subagent.StatusClosed {
			continue
		}
		owners = append(owners, info)
		rows = append(rows, i)
	}
	if len(owners) == 0 {
		return
	}
	counts, err := manager.ResponsibilityCounts(context.Background(), owners)
	if err != nil {
		// A count is advisory, so a store that could not be read leaves the
		// rows it covered empty instead of failing the member list.
		log.Printf("room %s: responsibility counts: %v", members[0].Address, err)
	}
	for _, i := range rows {
		if byKind := counts[members[i].ID]; len(byKind) > 0 {
			members[i].Responsibilities = byKind
		}
	}
}

// membersOf builds the member tree from a session list that includes closed
// sessions. A caller that also needs avatars shares one list scan.
func (s *Service) membersOf(rec room.Record, all []subagent.Info, rev string, withClosed bool) []Member {
	s.snap.mu.Lock()
	view := s.snap.view
	shared := view != nil && len(all) == len(view.all) && (len(all) == 0 || &all[0] == &view.all[0])
	s.snap.mu.Unlock()
	if shared {
		return s.memberItems(view, rec, withClosed)
	}
	return buildMemberItems(s, rec, all, rev, withClosed)
}

func (s *Service) buildMembers(rec room.Record, all []subagent.Info, rev string, withClosed bool) []Member {
	membership := subagent.RoomMembership(all, rec.ID, withClosed)
	// The owner row counts open top-level sessions only, even when closed ones are listed.
	top := 0
	for _, node := range membership.Roots {
		if node.Info.Role != subagent.RoleRoot && node.Info.Status != subagent.StatusClosed {
			top++
		}
	}
	seat := rec.Seat
	out := []Member{{Name: rec.Name, Address: rec.Name, Owner: true, Status: "owner", State: StateIdle, Children: top, Hue: NameHue(rec.Name), Seat: &seat}}
	var walk func([]*subagent.MemberNode, int)
	walk = func(nodes []*subagent.MemberNode, depth int) {
		for _, node := range nodes {
			out = append(out, s.member(node.Info, depth, len(node.Children)))
			walk(node.Children, depth+1)
		}
	}
	walk(membership.Roots, 0)
	book := s.avatarBook(rec.ID, all, rev)
	for i := range out {
		out[i].Emoji = book.byID[out[i].ID]
	}
	return out
}

func (s *Service) member(info subagent.Info, depth, kids int) Member {
	m := Member{
		ID: info.ID, Role: info.Role, Service: info.Service, Name: info.EffectiveName(), Address: info.Address(), Profile: info.Profile, Model: info.Model, Parent: info.ParentSession,
		Depth: depth, Children: kids, Status: info.Status, Turns: info.Turns, Created: info.Created, Activity: info.LastActivity,
		Finished: info.Status == subagent.StatusDormant, Resumable: info.Resumable && info.HarnessSession != "" && info.ConsumedBy == "",
		Closed: info.Status == subagent.StatusClosed, Hue: NameHue(info.Address()), OwnerDisplay: info.OwnerDisplay,
	}
	switch info.Status {
	case subagent.StatusDormant:
		m.Ended = info.Finished
	case subagent.StatusClosed:
		m.Ended = info.Closed
	}
	if info.Transfer != nil {
		m.Transfer = info.Transfer.Phase
	}
	m.State = StateIdle
	switch info.Status {
	case subagent.StatusRunning, subagent.StatusQueued:
		m.State = StateLive
		s.memberActivity(&m)
	case subagent.StatusClosed:
		m.State = StateClosed
	default:
		if info.Turns > 0 {
			if t, err := s.sessions().ReadTurn(info.ID, info.Turns); err == nil {
				switch t.Status {
				case subagent.TurnOK:
					m.State = StateDone
				case subagent.TurnError:
					m.State = StateError
				}
			}
		}
	}
	return m
}

func (s *Service) memberActivity(m *Member) {
	if m.Status == subagent.StatusRunning || m.Status == subagent.StatusQueued {
		if path, err := s.sessions().NormalizedPath(m.ID); err == nil {
			m.Doing = workeroutput.LastActivity(path)
		}
	}
}

// PostResult is the stored post and each recipient's delivery.
type PostResult struct {
	Message    room.Message      `json:"message"`
	Deliveries []notify.Delivery `json:"deliveries"`
	Failed     []notify.Delivery `json:"failed,omitempty"`
	// Sticky is the composer text to keep after a successful send: the
	// draft's explicit recipients (see Draft.Sticky).
	Sticky string `json:"sticky,omitempty"`
}
