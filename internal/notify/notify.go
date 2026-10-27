// Package notify routes room posts. It resolves mentions, stores each post
// once in exactly one room log, and delivers it to every recipient. It is the
// single delivery service behind `dyna post`, workflow post(), the desktop app, and
// automatic completions.
package notify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/parentoutput"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/visual"
)

// errNoRoom refuses to route a stored record that has no room. Such records
// come from an older dyna and are never sent to a default room.
var errNoRoom = errors.New("record has no room (created by an older dyna); refusing to route it")

// ErrDelivery marks a post that was stored but not delivered to every
// recipient. The result lists each recipient's outcome.
var ErrDelivery = errors.New("post was stored but not every delivery succeeded")

// Service resolves, stores, and delivers room posts.
type Service struct {
	questionMu   sync.Mutex
	questions    *questions.Store
	QuestionRoot string
	Rooms        *room.Store
	Sessions     *subagent.Manager
	// EngineRunning reports engine liveness; nil checks the engine socket.
	EngineRunning func() bool
}

// New pairs room routing and session execution. An explicit room store overrides
// the manager's room store. Other data stores share the session data root.
func New(rooms *room.Store, sessions *subagent.Manager) *Service {
	if sessions == nil {
		sessions = subagent.NewManager()
		if rooms != nil && rooms.Root != "" {
			sessions.Store = &subagent.Store{Root: filepath.Join(filepath.Dir(rooms.Root), "sessions")}
		}
	}
	if sessions.Store == nil {
		sessions.Store = subagent.DefaultStore()
	}
	if sessions.Store.Root == "" {
		sessions.Store.Root = runstore.SessionsDir()
	}
	if rooms == nil {
		rooms = sessions.RoomStore()
	}
	if rooms.Root == "" {
		rooms.Root = filepath.Join(filepath.Dir(sessions.Store.Root), "rooms")
	}
	sessions.Rooms = rooms
	s := &Service{Rooms: rooms, Sessions: sessions}
	// Keep the repairing store and the configured manager dependencies when reporting.
	if sessions.Store.ReportRepaired == nil {
		sessions.Store.ReportRepaired = func(store *subagent.Store, id string, turn int) {
			mgr := *sessions
			mgr.Store = store
			local := &Service{QuestionRoot: s.QuestionRoot, Rooms: s.Rooms, Sessions: &mgr}
			_, _ = local.CompleteSessionTurn(context.Background(), id, turn)
		}
	}
	return s
}

// Default returns a service over the default stores.
func Default() *Service { return New(nil, nil) }

func (s *Service) sessionStore() *subagent.Store {
	if s.Sessions != nil && s.Sessions.Store != nil {
		return s.Sessions.Store
	}
	return subagent.DefaultStore()
}

// SenderKind is the class of the process that posts.
type SenderKind int

const (
	// SenderOwner is the room owner: the human in the desktop app or an external
	// shell or orchestrator.
	SenderOwner SenderKind = iota
	// SenderSession is a stamped Dyna subagent session.
	SenderSession
	// SenderScript is a workflow script calling post().
	SenderScript
	// SenderVoice is Voice, the owner's voice call. It has no session; its
	// posts come from <room>:voice-call and carry room.SourceVoice.
	SenderVoice
	// SenderUnverified labels a question answer or withdrawal whose local
	// caller is not verified during the report-only rollout. It has no Room,
	// so question calls skip the room restriction during this rollout.
	// Enforced caller checks refuse it before a sender is made.
	// TODO: legacy/shim remove later — keeps unverified question calls working during report-only rollout; remove when callers are enforced.
	SenderUnverified
)

// Sender identifies who posts. Room is the room that bare mentions resolve
// in; it is empty for an unstamped external owner, who must qualify every
// mention.
type Sender struct {
	Kind    SenderKind
	Room    string
	Session subagent.Info
	RunID   string
	Agent   int
	// Human marks an owner who is a person typing in the desktop composer, not
	// an orchestrator process. Only owner posts carry it.
	Human bool
	// Card marks text that an HTML card wrote and the owner confirmed.
	Card bool
}

// source is the room.Message source of the sender's posts.
func (s Sender) source() string {
	if s.Kind == SenderVoice {
		return room.SourceVoice
	}
	if s.Card {
		return room.SourceCard
	}
	return ""
}

// SelfSender derives the posting identity of this process from its Dyna
// stamp. It never trusts a flag.
func (s *Service) SelfSender() (Sender, error) {
	if cur, ok := mode.CurrentSession(); ok {
		info, err := s.sessionStore().Info(cur.ID)
		if err != nil {
			return Sender{}, err
		}
		return Sender{Kind: SenderSession, Room: info.Room, Session: info}, nil
	}
	snd := Sender{Kind: SenderOwner}
	if stamp, ok := mode.StampFromEnv(); ok {
		snd.Room = stamp.Room
		if snd.Room == "" && stamp.RoomID != "" {
			rec, err := s.Rooms.Resolve(stamp.RoomID)
			if err != nil {
				return Sender{}, err
			}
			snd.Room = rec.Name
		}
	}
	return snd, nil
}

// ScriptSender is the identity of a workflow script's post() calls.
func (s *Service) ScriptSender(runID string) (Sender, error) {
	meta, err := runstore.ReadMeta(runID)
	if err != nil {
		return Sender{}, err
	}
	if meta.RoomID == "" || meta.Room == "" {
		return Sender{}, errNoRoom
	}
	return Sender{Kind: SenderScript, Room: meta.Room, RunID: runID}, nil
}

// PostInput is one post to resolve, store, and deliver.
type PostInput struct {
	// NoWake stores an informational post for the recipient's next turn.
	NoWake bool
	// NeedsRoot asks the root seat to act on this post.
	NeedsRoot bool
	// ID is a stable post ID for a retrying integration; empty generates one.
	ID string
	// Event is a typed system event. The registry then decides the post kind,
	// its text, its quiet flag and its wake rule, so the caller passes no Kind,
	// Notice or Text with it. A sealed family needs this field: Store.Post
	// rejects it.
	Event room.Event
	// Kind identifies an integration post; empty uses the normal message kind.
	Kind     string
	Notice   room.EventNotice
	Sender   Sender
	Mentions []string
	Text     string
	// Script is the run behind a workflow script's post, kept on the stored post.
	Script  *room.ScriptRef
	ReplyTo string
	// Conversation is where the writer wrote the post: the room feed, one
	// session's DM or the owner's voice call. Empty lets the store decide
	// (see room.HomeFor).
	Conversation room.Conversation
	// ForwardOf is the ID of a post in the recipients' room to forward. The
	// stored post carries a snapshot of it; its files are referenced, not copied.
	ForwardOf string
	// References are snapshots of the PRs and issues selected for the post.
	References []room.Reference
	// Attachments are already saved in the recipients' room.
	Attachments []room.Attachment
	// Files are saved in the recipients' room after the mentions resolve
	// and before the post is stored.
	Files []File
}

// Claim is who a posting process says it is: a session, a run's coordinator
// (Run, Agent 0), or the owner when neither is set. The engine checks it
// against the caller it resolved (requirements item 3) before it trusts it.
type Claim struct {
	Session string
	Run     string
	Agent   int
	Room    string
}

// ClaimOf is the claim for a sender from SelfSender.
func ClaimOf(snd Sender) Claim {
	switch snd.Kind {
	case SenderSession:
		return Claim{Session: snd.Session.ID, Room: snd.Room}
	}
	return Claim{Room: snd.Room}
}

// SenderOf is the sender a claim names. Only the engine's caller rules, or a
// test standing in for them, may trust a claim.
func (s *Service) SenderOf(c Claim) (Sender, error) {
	if c.Run != "" {
		return Sender{}, errors.New("a workflow run's coordinator posts only through its script's post()")
	}
	if c.Session == "" {
		return Sender{Kind: SenderOwner, Room: c.Room}, nil
	}
	info, err := s.sessionStore().Info(c.Session)
	if err != nil {
		return Sender{}, err
	}
	return Sender{Kind: SenderSession, Room: info.Room, Session: info}, nil
}

// PostRequest is one post that a CLI process asks the engine to store and
// deliver. It has no sender: the engine decides it from the caller.
type PostRequest struct {
	Kind string
	// Quiet stores the post as context for the recipient's next turn and wakes no one.
	Quiet     bool
	Mentions  []string
	Text      string
	ReplyTo   string
	ForwardOf string
	// Conversation is where the caller wrote the post, empty when it does not say.
	Conversation room.Conversation
	References   []room.Reference
	Files        []File
	Claim        Claim
}

// Input is the post that req asks for, from snd.
func (req PostRequest) Input(snd Sender) PostInput {
	return PostInput{Kind: req.Kind, NoWake: req.Quiet, Sender: snd, Mentions: req.Mentions, Text: req.Text, ReplyTo: req.ReplyTo, ForwardOf: req.ForwardOf, Conversation: req.Conversation, References: req.References, Files: req.Files}
}

// File is one attachment to save with a post.
type File struct {
	Name string
	Data []byte
	// Render "html" asks for an HTML card in the desktop (dyna post --html).
	Render string
	// Visual names the visualization a card updates (dyna post --update).
	Visual string
	// AllowNet lets the card load over HTTPS (dyna post --allow-net).
	AllowNet bool
	// Preview is the card's render and checks, made in the posting
	// process by visual.Preview.
	Preview visual.Result
}

// Delivery is what happened for one recipient.
type Delivery struct {
	To       string `json:"to"`
	Delivery string `json:"delivery"`
	Turn     int    `json:"turn,omitempty"`
	Pointer  bool   `json:"pointer,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Skip is a group member left out of a post at resolution time.
type Skip struct {
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Result is the stored post and how each recipient received it.
type Result struct {
	Message    room.Message `json:"message"`
	Deliveries []Delivery   `json:"deliveries"`
	Skipped    []Skip       `json:"skipped,omitempty"`
	// EngineDown: a post is queued for a session, but no engine is running to
	// deliver it. It waits until the engine starts.
	EngineDown bool `json:"engineDown,omitempty"`
}

// Failed reports the deliveries that failed.
func (r Result) Failed() []Delivery {
	var out []Delivery
	for _, d := range r.Deliveries {
		if d.Delivery == "failed" {
			out = append(out, d)
		}
	}
	return out
}

type recipientKind int

const (
	toOwner recipientKind = iota
	toSession
	// toVoice is Voice, the owner's voice call. A live call speaks the post.
	// The post does not create a call request, and no session wakes.
	toVoice
)

type recipient struct {
	kind    recipientKind
	addr    string
	rec     room.Record
	session subagent.Info
	runID   string
	agent   int
}

// Post resolves the mentions, stores one post in the recipients' room, and
// delivers it to each recipient. A resolution error stores nothing. After
// the post is stored, each delivery is best effort; if any fails, the error
// wraps ErrDelivery and the result names each failure.
func (s *Service) Post(ctx context.Context, in PostInput) (Result, error) {
	// Only the owner's own client says where it writes. A session, worker or
	// workflow cannot place its post in another session's DM or in the voice
	// call; for those senders room.HomeFor decides from the post itself.
	if in.Sender.Kind != SenderOwner {
		in.Conversation = ""
	}
	if in.Sender.Kind == SenderSession {
		meta, err := s.sessionStore().ReadMeta(in.Sender.Session.ID)
		if err != nil {
			return Result{}, err
		}
		in.Sender.Session.Meta = meta
	}
	// A typed event carries its own kind, notice, text and quiet flag: the
	// registry sets them and the caller cannot.
	wake := !in.NoWake
	if in.Event != nil {
		if in.Kind != "" || in.Notice.Kind != "" || in.Text != "" {
			return Result{}, errors.New("an event post carries no kind, notice or text of its own")
		}
		desc, ok := room.DescribeEvent(in.Event)
		if !ok {
			return Result{}, errors.New("the event is not registered in internal/room/events.go")
		}
		in.Kind, in.Notice.Kind, in.Text = desc.Kind, string(desc.Type), desc.Text
		// A post that needs root wakes the root seat, as it always has. The
		// spec decides for every other post.
		wake = !in.NoWake && (in.NeedsRoot || desc.Wake == room.WakeTarget)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" && len(in.References) == 0 && len(in.Attachments) == 0 && len(in.Files) == 0 && in.ForwardOf == "" {
		return Result{}, errors.New("post text is required")
	}
	if !utf8.ValidString(text) {
		return Result{}, errors.New("post text must be valid UTF-8")
	}
	if len(text) > room.MaxMessageBytes {
		return Result{}, fmt.Errorf("post is too long (maximum %s)", settings.FormatBytes(int64(room.MaxMessageBytes)))
	}
	res, err := s.resolve(in.Sender, in.Mentions)
	if err != nil {
		return Result{}, err
	}
	if err := s.servicePost(in, res); err != nil {
		return Result{}, err
	}
	if in.Sender.Kind == SenderSession && in.Sender.Session.Service && in.Kind == serviceAlertKind {
		dir, err := s.sessionStore().Dir(in.Sender.Session.ID)
		if err != nil {
			return Result{}, err
		}
		unlock, err := store.Lock(filepath.Join(dir, "service-alert.lock"))
		if err != nil {
			return Result{}, err
		}
		defer unlock()
	}
	previous, err := s.previousServiceAlert(in, res, text)
	if err != nil {
		return Result{}, err
	}
	if previous.ID != "" {
		return Result{Message: previous}, nil
	}
	var fwd *room.Forward
	if in.ForwardOf != "" {
		orig, err := s.Rooms.Message(res.rec.ID, in.ForwardOf)
		if err != nil {
			return Result{}, fmt.Errorf("cannot forward: %w", err)
		}
		fwd = room.NewForward(orig)
	}
	turn := 0
	if in.Sender.Kind == SenderSession {
		turn = in.Sender.Session.CurrentTurn
	}
	atts := append([]room.Attachment(nil), in.Attachments...)
	if len(atts)+len(in.Files) > room.MaxPostAttachments {
		return Result{}, fmt.Errorf("a post can carry at most %d attachments", room.MaxPostAttachments)
	}
	// Check rich files before saving anything, so a refused card leaves no
	// bytes behind. Post checks again from the saved bytes.
	cards := 0
	for _, f := range in.Files {
		if f.Render == room.RenderHTML {
			if cards++; cards > 1 {
				return Result{}, errors.New("a post can carry at most one HTML card")
			}
			if _, err := room.ParseRich(f.Name, f.Data); err != nil {
				return Result{}, fmt.Errorf("%s: %w", f.Name, err)
			}
			if text == "" {
				return Result{}, errors.New("a post with an HTML card needs text that sums it up for readers outside the desktop")
			}
		}
	}
	for _, f := range in.Files {
		a, err := s.Rooms.PutRenderedAttachment(res.rec.ID, f.Name, f.Data, f.Render)
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", f.Name, err)
		}
		a.Render, a.Visual, a.Net = f.Render, f.Visual, f.AllowNet
		if f.Render == room.RenderHTML {
			// The preview is optional: a preview that cannot be saved never
			// blocks the post.
			if err := s.savePreview(res.rec.ID, &a, f.Preview); err != nil {
				a.Banner, a.Thumb, a.Shots = "", "", nil
				a.Checks = visual.Skipped("preview not saved: " + err.Error())
			}
		}
		atts = append(atts, a)
	}
	var sessionIDs map[string]string
	for _, recipient := range res.recipients {
		if recipient.kind == toSession {
			if sessionIDs == nil {
				sessionIDs = map[string]string{}
			}
			sessionIDs[recipient.addr] = recipient.session.ID
		}
	}
	// The sender's stored room travels with the post, so root's wake decision
	// reads an identity rather than the room part of a display address.
	senderRoomID := ""
	if in.Sender.Kind == SenderSession {
		if sessionIDs == nil {
			sessionIDs = map[string]string{}
		}
		sessionIDs[res.from] = in.Sender.Session.ID
		senderRoomID = in.Sender.Session.RoomID
	}
	// The owner writing from one room to another speaks as the room they write
	// from, so a reply to the post (ReplyAddress) names that room's owner and
	// lands in the chat the owner wrote in.
	var origin room.Record
	originReply := ""
	crossRoom := in.Sender.Kind == SenderOwner && in.Sender.Human && in.Event == nil && in.Sender.Room != "" && in.Sender.Room != res.rec.Name
	if crossRoom {
		if origin, err = s.Rooms.Resolve(in.Sender.Room); err != nil {
			return Result{}, err
		}
		res.from, senderRoomID = origin.Name, origin.ID
		// The writer's conversation and reply target live in the origin room.
		originReply = in.ReplyTo
		in.Conversation, in.ReplyTo = "", ""
	}
	var msg room.Message
	if in.Event != nil {
		// A notice without a stable ID is a fresh post, as Store.Post makes it.
		msg, err = s.Rooms.PostEventInput(room.PostEventInput{RoomID: res.rec.ID, Event: in.Event, ID: in.ID, FreshID: in.ID == "", To: res.addrs(), Via: res.via,
			NeedsRoot: in.NeedsRoot, RoomService: in.Sender.Kind == SenderOwner && !in.Sender.Human, SessionIDs: sessionIDs, SenderRoomID: senderRoomID, NoWake: !wake})
	} else {
		msg, err = s.Rooms.Post(res.rec.ID, room.PostInput{Wake: wake, SessionIDs: sessionIDs, SenderRoomID: senderRoomID,
			ID: in.ID, NeedsRoot: in.NeedsRoot, Conversation: in.Conversation,
			From: res.from, Kind: in.Kind, Notice: in.Notice, Human: in.Sender.Kind == SenderOwner && in.Sender.Human, Source: in.Sender.source(), Script: in.Script, To: res.addrs(), Via: res.via, ReplyTo: in.ReplyTo, Forward: fwd, Turn: turn, Text: text,
			Attachments: atts, References: in.References,
		})
	}
	if err != nil {
		return Result{}, err
	}
	if in.Sender.Kind == SenderSession && turn > 0 {
		if _, err := s.sessionStore().UpdateTurn(in.Sender.Session.ID, turn, func(t *subagent.Turn) {
			for _, addr := range res.addrs() {
				if !slices.Contains(t.PostedTo, addr) {
					t.PostedTo = append(t.PostedTo, addr)
				}
			}
		}); err != nil {
			return Result{Message: msg}, err
		}
	}
	out := Result{Message: msg, Skipped: res.skipped}
	if crossRoom {
		// The sender's chat shows the post at once. The copy shares the post's
		// ID, so a reply to the post answers it in the sender's room too. It
		// wakes nobody: the recipients read the post in their own room.
		if _, err := s.Rooms.Post(origin.ID, room.PostInput{ID: msg.ID, From: origin.Name, Human: true, To: res.addrs(), Via: res.via, ReplyTo: originReply,
			Text: text, Attachments: atts, References: in.References}); err != nil {
			out.Deliveries = append(out.Deliveries, Delivery{To: origin.Name, Delivery: "failed", Error: "the post was sent, but its copy in " + origin.Name + " was not saved: " + err.Error()})
		}
	}
	for _, r := range res.recipients {
		out.Deliveries = append(out.Deliveries, s.deliver(msg, r))
	}
	if d, ok := s.deliverRoot(ctx, msg, res.rec); ok {
		out.Deliveries = append(out.Deliveries, d)
	}
	out.EngineDown = s.engineDown(out)
	return out, DeliveryError(out)
}

// DeliveryError reports the deliveries a stored post did not reach. A caller
// that treats a stored post as sent returns this so nobody sends it twice.
func DeliveryError(out Result) error {
	failed := out.Failed()
	if len(failed) == 0 {
		return nil
	}
	parts := make([]string, len(failed))
	for i, d := range failed {
		parts[i] = d.To + ": " + d.Error
	}
	return fmt.Errorf("%w: post %s; %d of %d failed (%s)", ErrDelivery, out.Message.ID, len(failed), len(out.Deliveries), strings.Join(parts, "; "))
}

// engineDown reports a session-queued delivery with no engine to act on it.
func (s *Service) engineDown(out Result) bool {
	if !slices.ContainsFunc(out.Deliveries, func(d Delivery) bool { return d.Delivery == "session-queued" }) {
		return false
	}
	if s.EngineRunning != nil {
		return !s.EngineRunning()
	}
	return !engineclient.Running("")
}

// deliver hands one stored post to one recipient.
func (s *Service) deliver(msg room.Message, r recipient) Delivery {
	d := Delivery{To: r.addr}
	opts := room.FrameOptions{Room: r.rec.Name, Reader: r.addr, Reply: s.sessionStore().ReplyAddress(msg)}
	if r.kind == toSession {
		opts.Reply, opts.ReplyTo = r.session.ServiceContract().FrameReply(msg, opts.Reply)
	}
	if len(msg.Attachments) > 0 {
		opts.FilesDir, _ = s.Rooms.FilesDir(r.rec.ID)
	}
	switch r.kind {
	case toOwner, toVoice:
		d.Delivery = "room"
	case toSession:
		// The engine observes the stored post and starts or steers its target.
		// The receipt means storage succeeded, even while the engine is down.
		d.Delivery = "session-queued"
	}
	return d
}

func completionKind(status string) string {
	switch status {
	case subagent.TurnOK:
		return "completed"
	case "canceled", "cancelled", "stopped":
		return "stopped"
	default:
		return "failed"
	}
}

// CompletionText is the body of a session completion post: a header, a
// result and a pointer to the full output. Markdown block
// structure survives (paragraphs, lists, tables, fences), and
// a one-line result stays on the header line. CompleteSessionTurn saves long
// reports before replacing them with a bounded prefix and file pointer.
func CompletionText(address string, turn int, kind string, result any, errText string) string {
	text := fmt.Sprintf("%s turn %d %s.", address, turn, kind)
	pointer := "Full output: dyna subagent output " + address
	if errText != "" {
		return text + " Error: " + errText + " " + pointer
	}
	var preview string
	if str, ok := result.(string); ok {
		preview = blockPreview(str, len(str))
	} else {
		preview = resultPreview(result, int(^uint(0)>>1))
	}
	switch {
	case preview == "":
		return text + " " + pointer
	case !strings.Contains(preview, "\n"):
		return text + " Result: " + preview + " " + pointer
	}
	first, rest, _ := strings.Cut(preview, "\n\n")
	if inlineBlock(first) {
		text += " Result: " + first
		if rest != "" {
			text += "\n\n" + rest
		}
	} else {
		text += " Result:\n\n" + preview
	}
	return text + "\n\n" + pointer
}

var tableSepLine = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

func isTableBlock(lines []string) bool {
	return len(lines) >= 2 && strings.Contains(lines[0], "|") && strings.Contains(lines[1], "-") && tableSepLine.MatchString(lines[1])
}

// inlineBlock reports whether a block is plain prose that can share the
// header's line: no list, heading, quote, table or code.
func inlineBlock(b string) bool {
	lines := strings.Split(b, "\n")
	if isTableBlock(lines) {
		return false
	}
	t := strings.TrimSpace(lines[0])
	if t == "" || strings.ContainsAny(t[:1], "#-*+>|`") {
		return false
	}
	if i := strings.IndexAny(t, ".)"); i > 0 && i <= 3 {
		if _, err := strconv.Atoi(t[:i]); err == nil {
			return false
		}
	}
	return true
}

var spaceRun = regexp.MustCompile(`[ \t]{2,}`)

// blockPreview keeps line structure, trims noise, and fits whole markdown
// blocks into limit bytes. A table that does not fit keeps its header and the
// rows that do; any cut ends with "...".
func blockPreview(text string, limit int) string {
	var blocks []string
	var cur []string
	fence := false
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
		} else if !fence {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			line = indent + spaceRun.ReplaceAllString(strings.TrimLeft(line, " \t"), " ")
		}
		if line == "" && !fence {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	var out string
	for _, b := range blocks {
		sep := 0
		if out != "" {
			sep = 2
		}
		if len(out)+sep+len(b) <= limit {
			if sep > 0 {
				out += "\n\n"
			}
			out += b
			continue
		}
		if part := fitBlock(b, limit-len(out)-sep); part != "" {
			if sep > 0 {
				out += "\n\n"
			}
			out += part
		}
		if strings.Contains(out, "\n") {
			out += "\n\n..."
		} else {
			out += "..."
		}
		return out
	}
	return out
}

// fitBlock returns the part of one block that fits in room bytes: a table's
// header with whole rows, other text by whole lines, a lone long line cut.
func fitBlock(b string, space int) string {
	lines := strings.Split(b, "\n")
	if isTableBlock(lines) {
		used := len(lines[0]) + 1 + len(lines[1])
		if used > space {
			return ""
		}
		kept := lines[:2]
		for _, row := range lines[2:] {
			if used+1+len(row) > space {
				break
			}
			used += 1 + len(row)
			kept = append(kept, row)
		}
		if len(kept) == 2 {
			return ""
		}
		return strings.Join(kept, "\n")
	}
	if len(b) <= space {
		return b
	}
	if space <= 0 {
		return ""
	}
	var kept []string
	used := 0
	for _, line := range lines {
		if used+len(line)+1 > space {
			break
		}
		used += len(line) + 1
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return room.TruncateUTF8(lines[0], space)
	}
	return strings.Join(kept, "\n")
}

func resultPreview(v any, limit int) string {
	var text string
	switch x := v.(type) {
	case nil:
		text = ""
	case string:
		text = x
	default:
		b, _ := json.Marshal(x)
		text = string(b)
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return room.TruncateUTF8(text, limit) + "..."
}

// complete stores one completion record addressed to each report target and
// delivers it. A target is a session address or the room name (the owner).
// A session target that is closed or in another room falls back to the
// owner.
func (s *Service) complete(ctx context.Context, roomID, from string, targets []string, kind, text string, turn int) (Result, error) {
	return s.completeTargets(ctx, roomID, from, targets, kind, text, turn, true)
}

func (s *Service) completeTargets(ctx context.Context, roomID, from string, targets []string, kind, text string, turn int, includeOwner bool) (Result, error) {
	plan, err := s.prepareCompletion(roomID, from, targets, kind, text, turn, includeOwner)
	if err != nil || plan == nil {
		return Result{}, err
	}
	return s.appendCompletion(ctx, *plan)
}

func (s *Service) prepareCompletion(roomID, from string, targets []string, kind, text string, turn int, includeOwner bool) (*subagent.ReportPlan, error) {
	rec, plan, err := s.planCompletion(roomID, targets, includeOwner)
	if err != nil || plan == nil {
		return nil, err
	}
	return &subagent.ReportPlan{RoomID: rec.ID, Post: room.PostInput{
		Wake: true, SessionIDs: plan.sessionIDs, SenderRoomID: roomID, From: from, Kind: kind, To: plan.addrs, Via: plan.via, Turn: turn, OwnerReport: plan.ownerReport, Text: text,
	}}, nil
}

// prepareEventCompletion plans a typed system event with the same recipients,
// mention tokens and owner report as a completion post. The registry writes the
// post kind and its text, so the poster cannot choose them.
func (s *Service) prepareEventCompletion(roomID string, ev room.Event, targets []string, turn int, includeOwner bool) (*subagent.ReportPlan, error) {
	rec, plan, err := s.planCompletion(roomID, targets, includeOwner)
	if err != nil || plan == nil {
		return nil, err
	}
	desc, ok := room.DescribeEvent(ev)
	if !ok {
		return nil, fmt.Errorf("event %T is not registered in internal/room/events.go", ev)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	return &subagent.ReportPlan{RoomID: rec.ID, Payload: payload, Post: room.PostInput{
		Wake: true, SessionIDs: plan.sessionIDs, SenderRoomID: roomID, Kind: desc.Kind, Notice: room.EventNotice{Kind: string(desc.Type)}, To: plan.addrs, Via: plan.via, Turn: turn, OwnerReport: plan.ownerReport, Text: desc.Text,
	}}, nil
}

// completionPlan is who receives a completion and how the post marks them.
type completionPlan struct {
	addrs       []string
	via         []string
	sessionIDs  map[string]string
	ownerReport bool
}

// planCompletion resolves a completion's recipients. A target is a session
// address or the room name (the owner). A session target that is closed or in
// another room falls back to the owner.
func (s *Service) planCompletion(roomID string, targets []string, includeOwner bool) (room.Record, *completionPlan, error) {
	if roomID == "" {
		return room.Record{}, nil, errNoRoom
	}
	rec, err := s.Rooms.Resolve(roomID)
	if err != nil {
		return room.Record{}, nil, err
	}
	var recipients []recipient
	var via []string
	seen := map[string]bool{}
	// The room owner is always a recipient: in the desktop the owner runs
	// the harness and sees every completion. Delivery to the owner wakes
	// no session. Failure alerts, run and shell targets may wake a parent session.
	ownerReport := slices.Contains(targets, rec.Name)
	if includeOwner {
		targets = append(append([]string(nil), targets...), rec.Name)
	}
	for _, target := range targets {
		r := recipient{kind: toOwner, addr: rec.Name, rec: rec}
		if target != rec.Name {
			if info, err := s.sessionStore().DeliveryTarget(target); err == nil && info.Status != subagent.StatusClosed && info.RoomID == rec.ID {
				r = recipient{kind: toSession, addr: info.Address(), rec: rec, session: info}
			} else if !includeOwner {
				continue
			}
		}
		if seen[r.addr] {
			continue
		}
		seen[r.addr] = true
		recipients = append(recipients, r)
		if r.kind == toOwner {
			via = append(via, "@root")
		} else {
			via = append(via, "@parent")
		}
	}
	if len(recipients) == 0 {
		return room.Record{}, nil, nil
	}
	addrs := make([]string, len(recipients))
	for i, r := range recipients {
		addrs[i] = r.addr
	}
	sessionIDs := map[string]string{}
	for _, recipient := range recipients {
		if recipient.kind == toSession {
			sessionIDs[recipient.addr] = recipient.session.ID
		}
	}
	if len(sessionIDs) == 0 {
		sessionIDs = nil
	}
	return rec, &completionPlan{addrs: addrs, via: via, sessionIDs: sessionIDs, ownerReport: ownerReport}, nil
}

// appendCompletion uses the exact recipient IDs saved with the plan. A
// renamed or closed recipient never binds the post to a new session. A plan
// that keeps a typed event posts it through PostEvent, so a replayed plan
// stores the same event.
func (s *Service) appendCompletion(ctx context.Context, plan subagent.ReportPlan) (Result, error) {
	rec, err := s.Rooms.Resolve(plan.RoomID)
	if err != nil {
		return Result{}, err
	}
	var msg room.Message
	if plan.Service != nil {
		// A service failure is one typed alert: the registry sets its kind,
		// text, quiet flag and wake rule, and the engine is its sender.
		msg, err = s.Rooms.PostEventInput(room.PostEventInput{
			RoomID:     plan.RoomID,
			Event:      *plan.Service,
			ID:         plan.Post.ID,
			To:         plan.Post.To,
			SessionIDs: plan.Post.SessionIDs,
			Producer:   serviceProducer(plan.Service.Service),
		})
	} else if len(plan.Payload) > 0 {
		ev, decodeErr := room.EventFromPayload(plan.Post.Kind, plan.Post.Notice.Kind, plan.Payload)
		if decodeErr != nil {
			return Result{}, decodeErr
		}
		// The plan's ID is keyed by session ID and turn, so two sessions that
		// share an address keep their own posts: the registry's own key hashes
		// the address, which a reused name would collide on.
		msg, err = s.Rooms.PostEventInput(room.PostEventInput{RoomID: plan.RoomID, Event: ev, ID: plan.Post.ID, Producer: plan.Producer,
			To: plan.Post.To, Via: plan.Post.Via, NeedsRoot: plan.Post.NeedsRoot, SessionIDs: plan.Post.SessionIDs, Turn: plan.Post.Turn, OwnerReport: plan.Post.OwnerReport,
			Conversation: string(plan.Post.Conversation)})
	} else {
		msg, err = s.Rooms.Post(plan.RoomID, plan.Post)
	}
	if err != nil {
		return Result{}, err
	}
	out := Result{Message: msg}
	for _, address := range msg.To {
		r := recipient{kind: toOwner, addr: address, rec: rec}
		if id := msg.SessionIDs[address]; id != "" {
			r.kind = toSession
			r.session = subagent.Info{Meta: subagent.Meta{ID: id, RoomID: rec.ID}}
			if current, err := s.sessionStore().DeliveryTarget(id); err == nil && current.RoomID == rec.ID {
				r.session = current
			}
		}
		out.Deliveries = append(out.Deliveries, s.deliver(msg, r))
	}
	if d, ok := s.deliverRoot(ctx, msg, rec); ok {
		out.Deliveries = append(out.Deliveries, d)
	}
	out.EngineDown = s.engineDown(out)
	return out, DeliveryError(out)
}

// parentTargets is the default report target of a run or shell: its parent
// session, or the owner when it has none.
func (s *Service) parentTargets(roomName, parentSession string) []string {
	if parentSession == "" {
		return []string{roomName}
	}
	return []string{parentSession}
}

// sessionCompletionKind distinguishes an actionable error from cancellation.
// A stopped turn may contain a concrete provider/schema/timeout error; retain
// that failure. User cancellation and forced stop repair are not failures.
func sessionCompletionKind(turn subagent.Turn) string {
	kind := completionKind(turn.Status)
	if kind == "completed" {
		return kind
	}
	message := strings.TrimSpace(turn.Error)
	cancelled := message == context.Canceled.Error() || strings.HasSuffix(message, ": "+context.Canceled.Error())
	if turn.StopAsked {
		cancelled = cancelled || message == "signal: killed" || message == "signal: terminated" || message == "turn runner exited without recording a result"
	}
	if kind == "stopped" && message != "" && !cancelled {
		return "failed"
	}
	if kind == "failed" && turn.StopAsked && cancelled {
		return "stopped"
	}
	return kind
}

// sessionParentTargets uses the current stored hierarchy, never ReportTo.
// A parent without an exact conversation cannot act on the alert. A transfer
// is temporary: keep addressing that parent so its replacement reads the post.
func (s *Service) sessionParentTargets(meta subagent.Meta) []string {
	if meta.ParentSession == "" || meta.ParentSession == meta.ID {
		return nil
	}
	parent, err := s.sessionStore().DeliveryTarget(meta.ParentSession)
	if err != nil || parent.Status == subagent.StatusClosed || parent.RoomID != meta.RoomID {
		return nil
	}
	if !parent.Transfer.Active() && (!parent.Resumable || (parent.Status == subagent.StatusIdle || parent.Status == subagent.StatusDormant) && parent.HarnessSession == "") {
		return nil
	}
	return []string{parent.ID}
}

// turnHasResult reports whether the turn ended with a final result of its own.
func turnHasResult(turn subagent.Turn) bool {
	switch result := turn.Result.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(result) != ""
	}
	return true
}

func silentSessionCompletion(turn subagent.Turn) bool {
	return turn.WrapUp || turn.StopReason == "transfer" || turn.StopReason == "consolidation" || turn.StopReason == "close"
}

// ServiceAlertKind is the post kind of a service alert a session writes: an
// actionable problem a service session reports to its owner or parent. It is
// not a system event, so it carries the session's own sender. It is named
// apart from the sealed "alert" family, which only the engine posts through
// PostEvent.
const ServiceAlertKind = "service-alert"

// serviceFailureKind marks the plan of a failed service turn. appendCompletion
// replaces it with the typed service/failed event, so the kind never reaches
// the store.
const serviceFailureKind = "service-failure"

// serviceAlertKind is the short name the notify package uses internally.
const serviceAlertKind = ServiceAlertKind

// serviceRoleName names the service an alert is about. A helper has no role of
// its own, so it reports as a helper of its service.
func serviceRoleName(meta subagent.Meta) string {
	if meta.Role != "" {
		return meta.Role
	}
	return "helper"
}

// serviceAlertRole names the service an alert is about and says whether
// `dyna services` can act on it. A helper reports its service's role, because
// that is the service the reader can start or read.
func (s *Service) serviceAlertRole(meta subagent.Meta) (string, bool) {
	if meta.Role != "" {
		return meta.Role, true
	}
	if parent, err := s.sessionStore().ReadMeta(meta.ParentSession); err == nil && parent.ManagedService() && parent.Role != "" {
		return parent.Role, true
	}
	return serviceRoleName(meta), false
}

// serviceRole maps a service's role name to the typed role of its failure
// event. A helper has no role of its own, so it reports as a helper.
func serviceRole(role string) room.ServiceRole {
	switch room.ServiceRole(role) {
	case room.ServiceLibrarian:
		return room.ServiceLibrarian
	case room.ServiceReviewer:
		return room.ServiceReviewer
	}
	return room.ServiceHelper
}

// serviceProducer is the producer that posts a service's failure event. The
// spec accepts the service's own role and no other.
func serviceProducer(role room.ServiceRole) room.EventProducer {
	switch role {
	case room.ServiceLibrarian:
		return room.ProducerLibrarian
	case room.ServiceReviewer:
		return room.ProducerReviewer
	}
	return room.ProducerHelper
}

// serviceFailureText is the alert a failed service turn posts: what failed,
// then the session it ran as, then how to inspect it. A service changes with
// `dyna services`, so the alert offers no child-failure recovery.
func serviceFailureText(meta subagent.Meta, turn subagent.Turn, role string, manage bool) string {
	lines := []string{
		fmt.Sprintf("Service %s failed turn %d: %s", role, turn.N, strings.TrimSpace(turn.Error)),
		"Session: " + meta.Address(),
	}
	profile, model := turn.Profile, turn.Model
	if profile == "" {
		profile, model = meta.Profile, meta.Model
	}
	if profile != "" {
		line := "Profile: " + profile
		if model != "" {
			line += " / " + model
		}
		lines = append(lines, line)
	}
	if manage {
		lines = append(lines, "Inspect: dyna services log "+role+" · restart: dyna services restart "+role)
	}
	// The worker's own stderr is evidence, kept out of the failure's cause
	// text: it may hold harmless warnings on a turn that timed out (D-108).
	if stderr := strings.TrimSpace(turn.WorkerStderr); stderr != "" {
		lines = append(lines, "Worker stderr: "+stderr)
	}
	return strings.Join(lines, "\n")
}

// CompleteSessionTurn stores one recoverable report for each terminal turn.
// Successful owed turns report to their current parent, or the owner when no
// parent is available.
// Explicit turn stops report to the owner. Close, transfer and consolidation
// stops stay silent. An actionable failure reports to the current available
// parent and owner, including crash repairs and schema failures. Consecutive
// identical failures stay silent until the error changes or the session recovers. ReportTo only records the successful-report obligation.
func (s *Service) CompleteSessionTurn(ctx context.Context, sessionID string, turnNumber int) (Result, error) {
	store := s.sessionStore()
	info, err := store.Info(sessionID)
	if err != nil {
		return Result{}, err
	}
	if turnNumber <= 0 {
		turnNumber = info.Turns
	}
	unlock, err := store.LockReport(ctx, sessionID)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	turn, claimed, err := store.ClaimReport(ctx, sessionID, turnNumber)
	if err != nil {
		return Result{}, err
	}
	if !claimed {
		if !turn.Done() {
			return Result{}, fmt.Errorf("session %s turn %d is still %s", sessionID, turn.N, turn.Status)
		}
		return Result{}, nil
	}
	plan := turn.ReportPlan
	if plan == nil {
		plan, err = s.prepareSessionReport(info, turn)
		if err != nil {
			return Result{}, err
		}
		if plan == nil {
			return Result{}, store.FinishReport(ctx, sessionID, turnNumber)
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("session-completion/%s/%d", sessionID, turnNumber)))
		plan.Post.ID = fmt.Sprintf("msg_%x", sum[:8])
		saved, err := store.SaveReportPlan(ctx, sessionID, turnNumber, *plan)
		if err != nil {
			return Result{}, err
		}
		plan = &saved
	}
	result, err := s.appendCompletion(ctx, *plan)
	if result.Message.ID == "" {
		return result, err
	}
	// Wake and exact session IDs are part of the stored post: this is the
	// engine's durable delivery intent, even if it is not running yet.
	if finishErr := store.FinishReport(ctx, sessionID, turnNumber); finishErr != nil {
		return result, errors.Join(err, finishErr)
	}
	return result, err
}

// prepareSessionReport keeps the existing completion policies and formatter
// in one place. A nil plan is a deliberate silent completion.
func (s *Service) prepareSessionReport(info subagent.Info, turn subagent.Turn) (*subagent.ReportPlan, error) {
	store := s.sessionStore()
	if silentSessionCompletion(turn) {
		return nil, nil
	}
	kind := sessionCompletionKind(turn)
	// The session that receives this report asked for the stop, so a plain
	// stop needs no report back.
	if kind == "stopped" && turn.StopReason == "recipient" {
		return nil, nil
	}
	// A transfer replacement turn is exempt: its failure either starts the
	// written-handoff fallback, which the notice says to wait for, or ends the
	// move and needs the recovery notice. Suppression by a matching earlier
	// error would leave a parent who was told to wait without the final
	// report that asks for recovery.
	if kind == "failed" && turn.N > 1 && turn.TransferID == "" {
		previous, err := store.ReadTurn(info.ID, turn.N-1)
		// Finish each turn, but alert only once for the same error
		// across consecutive failed turns. Missing history must not hide it.
		if err == nil && previous.Reported && !silentSessionCompletion(previous) &&
			sessionCompletionKind(previous) == "failed" && strings.TrimSpace(previous.Error) == strings.TrimSpace(turn.Error) {
			return nil, nil
		}
	}
	// Read again after claiming: promotion or transfer can change metadata
	// after the turn was launched, or while Info repaired an orphaned turn.
	meta, err := store.ReadMeta(info.ID)
	if err != nil {
		return nil, err
	}
	// A run-owned session reports to its run: the coordinator reads the turn's
	// result, so nothing posts to the room and no parent or owner wakes.
	if meta.RunOwned() {
		return nil, nil
	}
	if contract := meta.ServiceContract(); contract.Active() && kind != "failed" {
		if kind == "completed" && contract.Helper() {
			return s.helperCompletion(meta, turn)
		}
		return nil, nil
	}
	// A service is outside the hierarchy, so its failure is one typed alert
	// from the engine to the room owner, never a child failure report.
	if kind == "failed" && meta.ManagedService() {
		role, manage := s.serviceAlertRole(meta)
		plan, err := s.prepareCompletion(meta.RoomID, meta.Address(), []string{meta.Room}, serviceFailureKind, serviceFailureText(meta, turn, role, manage), turn.N, true)
		if err != nil || plan == nil {
			return plan, err
		}
		profile, model := turn.Profile, turn.Model
		if profile == "" {
			profile, model = meta.Profile, meta.Model
		}
		plan.Service = &room.ServiceFailedEvent{
			Service: serviceRole(role),
			Session: meta.Address(),
			Turn:    turn.N,
			Error:   strings.TrimSpace(turn.Error),
			Profile: profile,
			Model:   model,
			// The worker's stderr is separate evidence for the card's
			// disclosure; the cause stays the timeout (D-108).
			WorkerStderr: strings.TrimSpace(turn.WorkerStderr),
			Detail:       plan.Post.Text,
		}
		return plan, nil
	}
	if len(turn.ReportTo) == 0 && kind == "completed" {
		return nil, nil
	}
	var targets []string
	if kind == "failed" {
		targets = s.sessionParentTargets(meta)
	}
	if kind == "completed" {
		targets = s.sessionParentTargets(meta)
		if len(targets) == 0 {
			targets = []string{meta.Room}
		}
		postedAddress := targets[0]
		// A parent that is the root seat has one address for a session's post: the
		// room's own (notify resolver). The completion target still names the root
		// session, so both spellings are the same recipient here.
		posted := []string{postedAddress}
		if parent, err := store.DeliveryTarget(postedAddress); err == nil {
			postedAddress = parent.Address()
			posted = []string{postedAddress}
			if parent.Role == subagent.RoleRoot && parent.RoomID == meta.RoomID {
				posted = append(posted, meta.Room)
			}
		}
		// An explicit post stands in for the report only when the turn ends with
		// no final text. A turn that wrote a final result still reports it, so a
		// question posted to the parent mid-turn cannot hide the completion.
		if !turnHasResult(turn) && slices.ContainsFunc(posted, func(addr string) bool { return slices.Contains(turn.PostedTo, addr) }) {
			return nil, nil
		}
		ev, err := s.turnEvent(meta, turn)
		if err != nil {
			return nil, err
		}
		// One plan per target set: the report is built once, so its spill is
		// written once.
		result, err := s.prepareEventCompletion(meta.RoomID, ev, targets, turn.N, false)
		if result == nil && err == nil {
			return s.prepareEventCompletion(meta.RoomID, ev, []string{meta.Room}, turn.N, false)
		}
		return result, err
	}
	if meta.ParentSession == "" || slices.Contains(turn.ReportTo, meta.Room) {
		targets = append(targets, meta.Room)
	}
	return s.prepareTurnReport(meta, turn, targets, true)
}

// turnEvent builds the typed end-of-turn event: the report the turn gave, the
// failure's error, and how to inspect or recover from it.
func (s *Service) turnEvent(meta subagent.Meta, turn subagent.Turn) (room.TurnEvent, error) {
	state := turnState(sessionCompletionKind(turn))
	summary, err := s.turnSummary(meta, turn)
	if err != nil {
		return room.TurnEvent{}, err
	}
	ev := room.TurnEvent{Session: meta.Address(), SessionID: meta.ID, Turn: turn.N, State: state, Summary: summary}
	if state == room.TurnFailed {
		ev.Error = strings.TrimSpace(turn.Error)
		ev.WorkerStderr = strings.TrimSpace(turn.WorkerStderr)
	}
	ev.Detail = s.turnReportDetail(meta, turn, state)
	return ev, nil
}

// prepareTurnReport plans the typed end-of-turn event with the recipients,
// owner report and wake the free-text completion post carried.
func (s *Service) prepareTurnReport(meta subagent.Meta, turn subagent.Turn, targets []string, includeOwner bool) (*subagent.ReportPlan, error) {
	ev, err := s.turnEvent(meta, turn)
	if err != nil {
		return nil, err
	}
	plan, err := s.prepareEventCompletion(meta.RoomID, ev, targets, turn.N, includeOwner)
	if plan != nil && meta.ServiceContract().Helper() {
		// A service helper's result is input for its parent service, not room
		// work. Its home is the helper's DM; To still delivers to the parent.
		plan.Post.Conversation = room.DMConversation(meta.ID)
	}
	return plan, err
}

// turnState names the state a completion kind reports in the typed event.
func turnState(kind string) room.TurnState {
	switch kind {
	case "failed":
		return room.TurnFailed
	case "stopped":
		return room.TurnStopped
	}
	return room.TurnCompleted
}

// turnSummary is the report a turn gave, bounded the way its completion text
// was: a body the inline limit fits, or its head and the file that holds the
// rest. A service reports the artifact it wrote instead of its own body.
func (s *Service) turnSummary(meta subagent.Meta, turn subagent.Turn) (string, error) {
	summary := ""
	switch result := turn.Result.(type) {
	case nil:
	case string:
		summary = strings.TrimSpace(result)
	default:
		if b, err := json.MarshalIndent(result, "", "  "); err == nil && string(b) != "null" {
			summary = "```json\n" + string(b) + "\n```"
		}
	}
	if summary == "" {
		return "", nil
	}
	return s.boundSessionReport(meta.ID, turn.N, summary)
}

// turnReportDetail is what an agent needs past the row's own facts: where the
// full output lives, and how to inspect the failure, recover it by hand, or
// learn that a written-handoff fallback is already starting.
func (s *Service) turnReportDetail(meta subagent.Meta, turn subagent.Turn, state room.TurnState) string {
	lines := []string{"Full output: dyna subagent output " + meta.Address()}
	if state != room.TurnFailed {
		return strings.Join(lines, "\n")
	}
	profile, model := turn.Profile, turn.Model
	if profile == "" {
		profile, model = meta.Profile, meta.Model
	}
	if profile != "" {
		line := "Failed model: " + profile
		if model != "" {
			line += " / " + model
		}
		lines = append(lines, line)
	}
	// The worker's own stderr is evidence, kept out of the failure's cause
	// text: it may hold harmless warnings on a turn that timed out (D-108).
	if stderr := strings.TrimSpace(turn.WorkerStderr); stderr != "" {
		lines = append(lines, "Worker stderr: "+stderr)
	}
	lines = append(lines,
		fmt.Sprintf("Inspect: dyna subagent output %s --turn %d --transcript", meta.ID, turn.N))
	if s.sessionStore().TransferFallbackFollows(meta, turn) {
		lines = append(lines, "A written-handoff fallback is starting in this session; no action is needed unless it fails.")
		return strings.Join(lines, "\n")
	}
	lines = append(lines,
		fmt.Sprintf("Recover: dyna subagent transfer %s --profile <working-profile>", meta.ID))
	return strings.Join(lines, "\n")
}

// boundSessionReport keeps helper and ordinary completion reports on one spill
// and fallback path without changing the full stored turn result.
func (s *Service) boundSessionReport(id string, turn int, text string) (string, error) {
	dir, err := s.sessionStore().Dir(id)
	if err != nil {
		return "", err
	}
	bounded, err := parentoutput.Text(filepath.Join(dir, "parent-output", fmt.Sprintf("turn-%d.txt", turn)), text)
	if err != nil {
		// Deliver a bounded fallback so a failed artifact write cannot
		// silently discard this completion.
		pointer := fmt.Sprintf("\n\nCould not save full report. Full output: dyna subagent output %s --turn %d", id, turn)
		prefix := []rune(text)
		budget := parentoutput.InlineCharLimit - len([]rune(pointer))
		bounded = string(prefix[:min(len(prefix), budget)]) + pointer
	}
	return bounded, nil
}

// CompleteRun posts a detached workflow run's terminal state to its immediate
// parent session, or to its room owner when an external orchestrator started
// it. The card, the agent text and the row's state all come from the typed
// run event.
func (s *Service) CompleteRun(ctx context.Context, runID string) (Result, error) {
	meta, err := runstore.ReadMeta(runID)
	if err != nil {
		return Result{}, err
	}
	if meta.Status == "running" {
		return Result{}, fmt.Errorf("run %s is still running", runID)
	}
	ev := room.RunEvent{RunID: meta.ID, Name: meta.Name, State: runState(completionKind(meta.Status)), Parent: meta.ParentSession}
	if !meta.StartedAt.IsZero() && !meta.EndedAt.IsZero() {
		ev.DurationMS = int(meta.EndedAt.Sub(meta.StartedAt).Milliseconds())
	}
	ev.Steps, ev.Agents = runCounts(runID)
	ev.Summary, ev.Detail = runResult(meta)
	plan, err := s.prepareEventCompletion(meta.RoomID, ev, s.parentTargets(meta.Room, meta.ParentSession), 0, true)
	if err != nil || plan == nil {
		return Result{}, err
	}
	plan.Producer = room.ProducerWorkflow
	return s.appendCompletion(ctx, *plan)
}

// runState names the state a run's completion kind reports.
func runState(kind string) room.RunState {
	switch kind {
	case "failed":
		return room.RunFailed
	case "stopped":
		return room.RunStopped
	}
	return room.RunCompleted
}

// runCounts reads how many agent calls a run recorded and how many worker
// sessions served them. A run whose records cannot be read reports none.
func runCounts(runID string) (steps, agents int) {
	entries, err := runstore.ReadJournal(runID)
	if err != nil {
		return 0, 0
	}
	ids := map[int]bool{}
	for _, entry := range entries {
		ids[entry.ID] = true
	}
	return len(entries), len(ids)
}

// runResult is the run's own result as a short line, or the cause of its
// failure, and the pointer an agent follows for the full output.
func runResult(meta runstore.Meta) (summary, detail string) {
	switch {
	case meta.Status == "canceled":
		summary = "Canceled."
	case meta.Error != "":
		summary = resultPreview(meta.Error, 600)
	default:
		result, _ := runstore.ReadResult(meta.ID)
		if strings.TrimSpace(result) != "" {
			var value any
			if json.Unmarshal([]byte(result), &value) == nil {
				summary = resultPreview(value, 900)
			}
		}
		if summary == "" {
			summary = "No result."
		}
	}
	return summary, "Full output: dyna runs show " + meta.ID
}

// ShellExit is one finished shell job, as its owner hears of it.
type ShellExit struct {
	ID, RoomID, Parent string
	Description        string
	// Command is the command's first word, which labels the job's row.
	Command string
	Code    int
	// Duration is how long the job ran, from its start to its exit.
	Duration time.Duration
	// Tail is the last lines of the job's output.
	Tail string
}

// CompleteShell posts a detached shell's exit to its parent session or room
// owner as a typed shell result: the job's label, its state, its exit code and
// duration, and its output behind the row's own toggle.
func (s *Service) CompleteShell(ctx context.Context, e ShellExit) (Result, error) {
	if e.RoomID == "" {
		return Result{}, errNoRoom
	}
	rec, err := s.Rooms.Resolve(e.RoomID)
	if err != nil {
		return Result{}, err
	}
	state := room.ShellCompleted
	if e.Code != 0 {
		state = room.ShellFailed
	}
	ev := room.ShellEvent{ShellID: e.ID, Label: shellLabel(e.Command, e.Description), State: state, ExitCode: e.Code,
		DurationMS: int(e.Duration.Milliseconds()), Output: outputLines(e.Tail), Parent: e.Parent,
		Detail: "Full output: dyna shell tail " + e.ID}
	// A command a session ran is that session's own: its result reaches the
	// session alone and lives in the session's DM, so the room chat never lists
	// it and root gets no copy. A shell with no session is the owner's command,
	// and its result goes to the room owner, where root reads it.
	plan, err := s.prepareEventCompletion(e.RoomID, ev, s.parentTargets(rec.Name, e.Parent), 0, e.Parent == "")
	if err != nil || plan == nil {
		return Result{}, err
	}
	plan.Producer = room.ProducerShell
	return s.appendCompletion(ctx, *plan)
}

// shellLabel labels a job's row: its command's first word and the short
// description its caller gave, or the one of them it has.
func shellLabel(command, description string) string {
	command, description = strings.TrimSpace(command), strings.TrimSpace(description)
	switch {
	case command == "" && description == "":
		return "shell"
	case command == "":
		return description
	case description == "" || strings.EqualFold(description, command):
		return command
	}
	return command + " · " + description
}

// outputLines splits a job's output tail into the lines its row shows.
func outputLines(tail string) []string {
	tail = strings.TrimRight(tail, "\n")
	if strings.TrimSpace(tail) == "" {
		return nil
	}
	return strings.Split(tail, "\n")
}

// FormatMessage renders one watch line as compact JSON.
func FormatMessage(msg room.Message) string {
	b, _ := json.Marshal(msg)
	return string(b)
}

// savePreview saves a card's preview images with the room and records them
// and the checks on the card.
func (s *Service) savePreview(roomID string, a *room.Attachment, p visual.Result) error {
	put := func(name string, data []byte) (string, error) {
		if len(data) == 0 {
			return "", nil
		}
		img, err := s.Rooms.PutAttachment(roomID, name, data)
		return img.ID, err
	}
	var err error
	if a.Banner, err = put("banner.webp", p.Banner); err != nil {
		return err
	}
	if a.Thumb, err = put("thumb.webp", p.Thumb); err != nil {
		return err
	}
	for _, shot := range p.Widths {
		id, err := put(fmt.Sprintf("width-%d.webp", shot.Width), shot.Image)
		if err != nil {
			return err
		}
		if id != "" {
			a.Shots = append(a.Shots, room.Shot{Width: shot.Width, ID: id})
		}
	}
	a.Checks = p.Checks
	return nil
}
