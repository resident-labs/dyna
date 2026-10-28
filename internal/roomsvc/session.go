package roomsvc

import (
	"strings"
	"unicode/utf8"

	"context"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"log"
	"path/filepath"
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
	"dyna-agent/internal/textutil"
	"dyna-agent/internal/transcript"
	"dyna-agent/internal/workeroutput"
)

// Session is a member's drill-down: identity plus transcript.
type Session struct {
	Cursor  string `json:"cursor,omitempty"`
	Earlier bool   `json:"earlier"`
	Newer   bool   `json:"newer"`
	Member  Member `json:"member"`
	Address string `json:"address"`
	Posts   []Post `json:"posts"`
	// References are the compact rows of the posts of other conversations that
	// this one received or wrote. Each row names its post, its sender and its
	// home, so a reader jumps to the post where it lives instead of drawing a
	// second copy.
	References []ReferenceRow     `json:"references,omitempty"`
	Media      []Post             `json:"media"`
	Dir        string             `json:"dir"`
	Usage      workeroutput.Usage `json:"usage"`
	Turns      []Turn             `json:"turns"`
}

// ReferenceRow is one post of another conversation, as a session's DM shows it:
// who wrote it, which conversation it is in, its typed event facts or text
// preview, and how many files it carries. TS is unix milliseconds.
type ReferenceRow struct {
	PostID string           `json:"postId"`
	From   string           `json:"from"`
	Kind   string           `json:"kind,omitempty"`
	Notice room.EventNotice `json:"notice,omitzero"`
	// Payload keeps a typed event's facts so the DM can use the same card as
	// the feed instead of reading the agent-facing text as a preview.
	Payload room.EventPayload `json:"payload,omitzero"`
	// Speaker is the sender's label (see SpeakerLabel).
	Speaker string `json:"speaker"`
	// FromHue is the sender's stable name color index (see NameHue).
	FromHue int `json:"fromHue"`
	// Conversation is the home of the post, such as "room" or "voice".
	Conversation string `json:"conversation"`
	TS           int64  `json:"ts"`
	Preview      string `json:"preview"`
	Attachments  int    `json:"attachments"`
}

// Turn is one transcript turn.
type Turn struct {
	WrapUp             bool                            `json:"wrapUp,omitempty"`
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
	Wake               *wakeView                       `json:"wake,omitempty"`
}

// wakeView is the typed wake cause the session view draws. Line is the short
// sentence; the other fields are the facts, so the view does not parse text.
type wakeView struct {
	Kind   string `json:"kind"`
	PostID string `json:"postId,omitempty"`
	Sender string `json:"sender,omitempty"`
	Name   string `json:"name,omitempty"`
	Line   string `json:"line"`
}

func wakeViewOf(c subagent.WakeCause) *wakeView {
	if c.Kind == "" {
		return nil
	}
	return &wakeView{Kind: string(c.Kind), PostID: c.PostID, Sender: c.Sender, Name: c.Name, Line: c.Line()}
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
	// Step is what a tool call did, in the session view's own words: read,
	// edit, search, run and so on. Label is the call's short name, a file name
	// or a command's first word, and ToolName the harness's own tool ID. The
	// row shows Step, Label and a state; ToolName, the full target and the exit
	// code stay in its details.
	Step       string `json:"step,omitempty"`
	Label      string `json:"label,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Target     string `json:"target,omitempty"`
	Aside      string `json:"aside,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
	Done       bool   `json:"done,omitempty"`
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
		item.Step, item.Label, item.ToolName = string(it.Tool.Step), toolLabel(it.Tool), it.Tool.Name
		item.DurationMS = int(it.Tool.Dur.Milliseconds())
		item.Aside, item.Failed, item.Done = transcript.ToolAside(it.Tool), it.Tool.Failed, it.Tool.Done
		item.JobID, item.JobOp = it.Tool.JobID, string(it.Tool.JobOp)
		if it.Tool.Failed {
			item.Command, item.Output, item.Exit = tail(it.Tool.Detail, maxFailedCommand), tail(ansi.Strip(it.Tool.Output), maxFailedOutput), it.Tool.Exit
		}
		if it.Tool.Name == "codemode" {
			item.Label, item.Command = "Code", it.Tool.Detail
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

// toolLabel is the short name a step row shows: a file's own name, a
// command's first word, or the pattern a search used. The full target and the
// harness's tool ID stay in the row's details.
func toolLabel(t transcript.Tool) string {
	target := strings.TrimSpace(t.Target)
	if target == "" {
		return ""
	}
	switch t.Step {
	case transcript.StepRun, transcript.StepMonitor, transcript.StepFetch, transcript.StepDelegate:
		if word, _, ok := strings.Cut(target, " "); ok {
			return word
		}
		return strings.TrimRight(target, "/")
	case transcript.StepRead, transcript.StepEdit, transcript.StepWrite, transcript.StepList:
		return filepath.Base(strings.TrimRight(target, "/"))
	}
	return target
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
		turn := Turn{WrapUp: t.WrapUp, Harness: t.Harness, OutputDialect: t.OutputDialect, UsageNormalization: t.UsageNormalization, Profile: t.Profile, Model: t.Model, HarnessSession: t.HarnessSession, N: t.N, Status: t.Status, Started: t.Started, Ended: t.Ended, Usage: t.Usage, Omitted: t.Omitted, Wake: wakeViewOf(t.Wake)}
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
	// The seat's DM keeps one conversation when its holder changes, so a view of
	// the seat reads that one wherever the seat is today.
	conversation := dmConversation(sess.Info)
	selected, err := s.dmPosts(rec, conversation, sess.Info, sess.Since, sess.Before)
	if err != nil {
		return Session{}, err
	}
	out.Posts, err = s.shapeSessionPosts(rec, selected, book)
	if err != nil {
		return Session{}, err
	}
	out.Posts, err = s.withItemPosts(rec, out.Posts, conversation, sess.Info.ID, sess.Info.Address(), out.Turns, book)
	if err != nil {
		return Session{}, err
	}
	rows, err := s.dmReferences(rec, conversation, sess.Since, sess.Before)
	if err != nil {
		return Session{}, err
	}
	out.References = rows
	out.Media, err = s.sessionMedia(rec, conversation, book)
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// authoredBy reports whether a post was written by one session: an agent post
// names it in the sender, and a typed turn event names it in the payload.
func authoredBy(msg room.Message, id, addr string) bool {
	if msg.SentBySession(id, addr) {
		return true
	}
	ev, ok := msg.TurnResult()
	return ok && ev.SessionID == id
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

// sessionMedia is every file of the whole DM, newest last: the files of the
// posts written in it, of the posts delivered to it and of the posts the
// conversation's own side wrote elsewhere. It reads the stored home and
// delivery rows, so it follows the same conversations the posts do.
func (s *Service) sessionMedia(rec room.Record, conversation room.Conversation, book avatarBook) ([]Post, error) {
	selected, err := s.rooms().Files(rec.ID, conversation, conversation.SessionID(), conversation.SessionID(), maxSessionMedia)
	if err != nil {
		return nil, err
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

// dmConversation is the DM a session view reads. The root seat's DM names the
// seat (room.SeatDMConversation), so it survives a change of holder; every
// other DM names its session.
func dmConversation(info subagent.Info) room.Conversation {
	if info.Role == subagent.RoleRoot || info.Name == room.RootAgent {
		return room.SeatDMConversation(info.RoomID)
	}
	return room.DMConversation(info.ID)
}

// dmPosts is the posts written in the session's DM within the loaded window.
// A completion record of this session is left out: it folds into its turn in
// the transcript, so the DM does not draw it twice.
func (s *Service) dmPosts(rec room.Record, conversation room.Conversation, info subagent.Info, since, before time.Time) ([]room.Message, error) {
	msgs, err := s.rooms().ConversationMessages(rec.ID, conversation, since, before, maxSessionPosts)
	if err != nil {
		return nil, err
	}
	out := make([]room.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.SentBySession(info.ID, info.Address()) && IsCompletion(m) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// dmReferences is the compact rows of the posts of other conversations that
// this one received or wrote. A DM names its own side by the same identity its
// rows and deliveries use: the session for a session's DM, and the seat for the
// root DM, so the seat's posts stay in it whoever holds the seat now. Rows a
// delivery and an author both name stay one row. A completion of the session
// whose DM this is folds into its turn in the transcript, so it is not a second
// row; the seat's own records stay, because they are the root's report to the
// owner and nothing else in this view draws them.
func (s *Service) dmReferences(rec room.Record, conversation room.Conversation, since, before time.Time) ([]ReferenceRow, error) {
	author := conversation.SessionID()
	received, err := s.rooms().ReceivedPosts(rec.ID, author, conversation, since, before, maxSessionPosts)
	if err != nil {
		return nil, err
	}
	authored, err := s.rooms().AuthoredPosts(rec.ID, author, conversation, since, before, maxSessionPosts)
	if err != nil {
		return nil, err
	}
	seat := room.SeatDMConversation(rec.ID)
	kept := make([]room.HomePost, 0, len(authored))
	for _, post := range authored {
		if conversation != seat && IsCompletion(post.Message) {
			continue
		}
		kept = append(kept, post)
	}
	posts := mergeHomePosts(received, kept)
	rows := make([]ReferenceRow, 0, len(posts))
	for _, post := range posts {
		m := post.Message
		rows = append(rows, ReferenceRow{
			PostID:       m.ID,
			From:         m.From,
			Kind:         m.Kind,
			Notice:       m.Notice,
			Payload:      m.Payload,
			Speaker:      SpeakerLabel(m, rec.Name),
			FromHue:      NameHue(m.From),
			Conversation: post.Home.String(),
			TS:           m.TS,
			Preview:      textutil.ClipOneLine(m.Text, maxReferencePreview),
			Attachments:  len(m.Attachments),
		})
	}
	return rows, nil
}

// mergeHomePosts keeps the newest posts of both reads, oldest last, each once.
func mergeHomePosts(first, second []room.HomePost) []room.HomePost {
	seen := make(map[string]bool, len(first)+len(second))
	out := make([]room.HomePost, 0, len(first)+len(second))
	for _, list := range [][]room.HomePost{first, second} {
		for _, post := range list {
			if seen[post.Message.ID] {
				continue
			}
			seen[post.Message.ID] = true
			out = append(out, post)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Message.TS < out[j].Message.TS })
	if len(out) > maxSessionPosts {
		out = out[len(out)-maxSessionPosts:]
	}
	return out
}

// homeMessages drops the home of each post, for a reader that only needs the
// posts.
func homeMessages(posts []room.HomePost) []room.Message {
	out := make([]room.Message, 0, len(posts))
	for _, post := range posts {
		out = append(out, post.Message)
	}
	return out
}

// withItemPosts adds the stored post of every turn item the selection left
// out, such as a post from before the page window. A turn item names the post
// that carried it, and that post is the message's one source: its text, its
// attachments, its forward and its references. Only a post of this DM joins
// the DM's posts: a post of another conversation shows as a reference row.
func (s *Service) withItemPosts(rec room.Record, posts []Post, conversation room.Conversation, id, address string, turns []Turn, book avatarBook) ([]Post, error) {
	have := make(map[string]bool, len(posts))
	for _, p := range posts {
		have[p.ID] = true
	}
	var missing []room.Message
	for _, t := range turns {
		for _, it := range t.Items {
			if it.PostID == "" || have[it.PostID] {
				continue
			}
			have[it.PostID] = true
			m, err := s.rooms().Message(rec.ID, it.PostID)
			if err != nil || s.rooms().HomeOf(rec.ID, it.PostID) != conversation.Home() {
				continue
			}
			if m.SentBySession(id, address) && IsCompletion(m) {
				continue
			}
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return posts, nil
	}
	extra, err := s.feedOf(rec, missing, 0, book, nil)
	if err != nil {
		return nil, err
	}
	out := append(posts, extra.Posts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

const (
	maxSessionPosts = 300
	maxSessionMedia = 200
	// maxReferencePreview is the longest one-line preview of a reference row.
	maxReferencePreview = 140
)

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

// RootSession is the root conversation shaped like any session view. With
// the internal root running it is that session's transcript. Otherwise
// (an external orchestrator holds the seat, or the internal root has not
// started) it has the seat holder and the seat's DM: the owner's posts to the
// seat, plus a reference row for every post that reaches the seat from another
// conversation.
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
	conversation := room.SeatDMConversation(rec.ID)
	msgs, err := s.rooms().ConversationMessages(rec.ID, conversation, time.Time{}, time.Time{}, maxSessionPosts)
	if err != nil {
		return Session{}, err
	}
	book := s.avatars(rec.ID)
	posts, err := s.shapeSessionPosts(rec, msgs, book)
	if err != nil {
		return Session{}, err
	}
	// The seat reads its own posts as the rows name them: the seat identity.
	rows, err := s.dmReferences(rec, conversation, time.Time{}, time.Time{})
	if err != nil {
		return Session{}, err
	}
	media, err := s.sessionMedia(rec, conversation, book)
	if err != nil {
		return Session{}, err
	}
	seat := rec.Seat
	member := Member{Role: subagent.RoleRoot, Name: room.RootAgent, Address: addr, Status: "seat", State: StateIdle, Hue: NameHue(rec.Name), Seat: &seat}
	return Session{Member: member, Address: addr, Posts: posts, References: rows, Media: media, Dir: rec.Repo, Turns: []Turn{}}, nil
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
	scanStores := func() {
		s.scanChangedPosts(offsets, emit)
		for topic := range s.takeStoreTopics() {
			switch topic {
			case "goals":
				s.scanGoals(goalRows, goalWork, &goalVersion, false, emit)
			case "questions":
				s.scanQuestions(questionSig, &questionVersion, false, emit)
			case "notices":
				s.scanNotices(&noticeVersion, false, emit)
			case "sync":
				s.scanSync(&syncVersion, false, emit)
			}
		}
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for n := 0; ; n++ {
		// TODO: legacy/shim remove later — fallback scans detect external writers until all mutations run in this engine.
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
		if !s.waitTick(ctx, tick.C, func() { s.scanPRs(prSig, prWork, &prVersion, false, emit) }, func() { s.scanDirtyMembers(memberSig, liveEmit) }, func() { scanCoordinators(s.takeCoordinatorHints()) }, func() { s.scanCallRequests(callRequestSig, &callRequestVersion, false, emit) }, scanStores) {
			return
		}
	}
}

// waitTick waits for the next tick. Local state wakes scan only the affected
// store between ticks. It
// returns false when ctx ends.
func (s *Service) waitTick(ctx context.Context, tick <-chan time.Time, scanPRs, scanSessions, scanCoordinators, scanCallRequests, scanStores func()) bool {
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
		case <-s.storeWake:
			scanStores()
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
		s.scanPosts(rec.ID, offsets, first, emit)
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

// scanPosts shares the byte cursor between committed wakes and fallback scans.
func (s *Service) scanPosts(roomID string, offsets map[string]int64, first bool, emit func(Event)) {
	store := s.rooms()
	size, err := store.FileSize(roomID)
	if err != nil {
		return
	}
	if first {
		offsets[roomID] = size
		return
	}
	prev := offsets[roomID] // a room created after the first scan starts at 0
	if size == prev {
		return
	}
	posts, next, err := s.FeedSince(roomID, prev)
	if err != nil {
		return
	}
	offsets[roomID] = next
	if len(posts) > 0 {
		emit(Event{Kind: "posts", RoomID: roomID, Posts: posts, Offset: next})
	}
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
