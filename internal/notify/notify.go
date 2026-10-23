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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	// SenderWorker is a workflow worker's shell.
	SenderWorker
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
}

// source is the room.Message source of the sender's posts.
func (s Sender) source() string {
	if s.Kind == SenderVoice {
		return room.SourceVoice
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
	if mode.InWorkflow() {
		// A missing or bad agent stamp parses as 0, which workerSender refuses.
		agent, _ := strconv.Atoi(strings.TrimSpace(os.Getenv(mode.EnvWorkerAgent)))
		stamp, _ := mode.StampFromEnv()
		return workerSender(strings.TrimSpace(os.Getenv(mode.EnvWorkerRun)), agent, stamp.Room)
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

// workerSender is agent of workflow run runID. Its room is the run's room,
// or room when the run's record cannot be read.
func workerSender(runID string, agent int, room string) (Sender, error) {
	if runID == "" || agent < 1 {
		return Sender{}, errors.New("this workflow worker has no run or agent stamp")
	}
	snd := Sender{Kind: SenderWorker, RunID: runID, Agent: agent, Room: room}
	if meta, err := runstore.ReadMeta(runID); err == nil {
		snd.Room = meta.Room
	}
	if snd.Room == "" {
		return Sender{}, errNoRoom
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
	ReplyTo  string
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

// Claim is who a posting process says it is: a session, a workflow worker
// (Run and Agent), or the owner when neither is set. The engine checks it
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
	case SenderWorker:
		return Claim{Run: snd.RunID, Agent: snd.Agent, Room: snd.Room}
	}
	return Claim{Room: snd.Room}
}

// SenderOf is the sender a claim names. Only the engine's caller rules, or a
// test standing in for them, may trust a claim.
func (s *Service) SenderOf(c Claim) (Sender, error) {
	if c.Run != "" {
		return workerSender(c.Run, c.Agent, c.Room)
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
	NeedsRoot bool
	Kind      string
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
	return PostInput{NeedsRoot: req.NeedsRoot, Kind: req.Kind, Sender: snd, Mentions: req.Mentions, Text: req.Text, ReplyTo: req.ReplyTo, ForwardOf: req.ForwardOf, Conversation: req.Conversation, References: req.References, Files: req.Files}
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
	toWorker
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
	if in.Sender.Kind == SenderSession && in.Sender.Session.Service && in.Kind == "alert" {
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
	if in.Sender.Kind == SenderSession {
		if sessionIDs == nil {
			sessionIDs = map[string]string{}
		}
		sessionIDs[res.from] = in.Sender.Session.ID
	}
	var msg room.Message
	if in.Event != nil {
		// A notice without a stable ID is a fresh post, as Store.Post makes it.
		msg, err = s.Rooms.PostEventInput(room.PostEventInput{RoomID: res.rec.ID, Event: in.Event, ID: in.ID, FreshID: in.ID == "", To: res.addrs(), Via: res.via,
			NeedsRoot: in.NeedsRoot, RoomService: in.Sender.Kind == SenderOwner && !in.Sender.Human, SessionIDs: sessionIDs, NoWake: !wake})
	} else {
		msg, err = s.Rooms.Post(res.rec.ID, room.PostInput{Wake: wake, SessionIDs: sessionIDs,
			ID: in.ID, NeedsRoot: in.NeedsRoot, Conversation: in.Conversation,
			From: res.from, Kind: in.Kind, Notice: in.Notice, Human: in.Sender.Kind == SenderOwner && in.Sender.Human, Source: in.Sender.source(), To: res.addrs(), Via: res.via, ReplyTo: in.ReplyTo, Forward: fwd, Turn: turn, Text: text,
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
	case toWorker:
		steer, pointer := msg.SteeringFrame(opts, runstore.MaxSteeringMessageBytes)
		if err := runstore.SubmitAgentSteering(r.runID, r.agent, steer); err != nil {
			d.Delivery, d.Error = "failed", err.Error()
			return d
		}
		d.Delivery, d.Pointer = "worker-steered", pointer
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
	if roomID == "" {
		return nil, errNoRoom
	}
	rec, err := s.Rooms.Resolve(roomID)
	if err != nil {
		return nil, err
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
		return nil, nil
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
	return &subagent.ReportPlan{RoomID: rec.ID, Post: room.PostInput{
		Wake: true, SessionIDs: sessionIDs, From: from, Kind: kind, To: addrs, Via: via, Turn: turn, OwnerReport: ownerReport, Text: text,
	}}, nil
}

// appendCompletion uses the exact recipient IDs saved with the plan. A
// renamed or closed recipient never binds the post to a new session.
func (s *Service) appendCompletion(ctx context.Context, plan subagent.ReportPlan) (Result, error) {
	rec, err := s.Rooms.Resolve(plan.RoomID)
	if err != nil {
		return Result{}, err
	}
	msg, err := s.Rooms.Post(plan.RoomID, plan.Post)
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

func silentSessionCompletion(turn subagent.Turn) bool {
	return turn.WrapUp || turn.StopReason == "transfer" || turn.StopReason == "consolidation" || turn.StopReason == "close"
}

// serviceAlertKind is the post kind of a service failure alert. The sealed
// system event registry replaces it with family "service", type "failed"
// (dyna:notice-contract, PR 5), so the kind and the text live in these two
// places only: one constant and one function.
const serviceAlertKind = "alert"

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
	if kind == "failed" && turn.N > 1 {
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
	if contract := meta.ServiceContract(); contract.Active() && kind != "failed" {
		if kind == "completed" && contract.Helper() {
			return s.helperCompletion(meta, turn)
		}
		return nil, nil
	}
	// A service is outside the hierarchy, so its failure is one alert from the
	// service to the room owner, never a child failure report.
	if kind == "failed" && meta.ManagedService() {
		role, manage := s.serviceAlertRole(meta)
		return s.prepareCompletion(meta.RoomID, meta.Address(), []string{meta.Room}, serviceAlertKind, serviceFailureText(meta, turn, role, manage), turn.N, true)
	}
	if len(turn.ReportTo) == 0 && kind == "completed" {
		return nil, nil
	}
	text := CompletionText(meta.Address(), turn.N, kind, turn.Result, turn.Error)
	var targets []string
	if kind == "failed" {
		targets = s.sessionParentTargets(meta)
		failedProfile, failedModel := turn.Profile, turn.Model
		if failedProfile == "" {
			failedProfile, failedModel = meta.Profile, meta.Model
		}
		if failedProfile != "" {
			text += "\nFailed model: " + failedProfile
			if failedModel != "" {
				text += " / " + failedModel
			}
		}
		text += fmt.Sprintf("\nInspect: dyna subagent output %s --turn %d --transcript\nRecover: dyna subagent transfer %s --profile <working-profile>", meta.ID, turn.N, meta.ID)
	}
	if kind == "completed" {
		targets = s.sessionParentTargets(meta)
		if len(targets) == 0 {
			targets = []string{meta.Room}
		}
		postedAddress := targets[0]
		if parent, err := store.DeliveryTarget(postedAddress); err == nil {
			postedAddress = parent.Address()
		}
		if slices.Contains(turn.PostedTo, postedAddress) {
			return nil, nil
		}
	}
	text, err = s.boundSessionReport(meta.ID, turn.N, text)
	if err != nil {
		return nil, err
	}
	if kind == "completed" {
		result, err := s.prepareCompletion(meta.RoomID, meta.Address(), targets, kind, text, turn.N, false)
		if result == nil && err == nil {
			return s.prepareCompletion(meta.RoomID, meta.Address(), []string{meta.Room}, kind, text, turn.N, false)
		}
		return result, err
	}
	if meta.ParentSession == "" || slices.Contains(turn.ReportTo, meta.Room) {
		targets = append(targets, meta.Room)
	}
	return s.prepareCompletion(meta.RoomID, meta.Address(), targets, kind, text, turn.N, true)
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
// it.
func (s *Service) CompleteRun(ctx context.Context, runID string) (Result, error) {
	meta, err := runstore.ReadMeta(runID)
	if err != nil {
		return Result{}, err
	}
	if meta.Status == "running" {
		return Result{}, fmt.Errorf("run %s is still running", runID)
	}
	kind := completionKind(meta.Status)
	text := fmt.Sprintf("run:%s %s.", runID, kind)
	if meta.Status == "canceled" {
		text += " Canceled."
	} else if meta.Error != "" {
		text += " Error: " + resultPreview(meta.Error, 600)
	} else if result, ok := runstore.ReadResult(runID); ok && result != "" {
		var value any
		if json.Unmarshal([]byte(result), &value) == nil {
			text += " Result: " + resultPreview(value, 900)
		}
	}
	text += fmt.Sprintf(" Full output: dyna runs show %s", runID)
	return s.complete(ctx, meta.RoomID, "run:"+runID, s.parentTargets(meta.Room, meta.ParentSession), kind, text, 0)
}

// ShellExit is one finished shell job, as its owner hears of it.
type ShellExit struct {
	ID, RoomID, Parent string
	Description        string
	Code               int
	// Tail is the last lines of the job's output.
	Tail string
}

// CompleteShell posts a detached shell's exit to its parent session or room
// owner, with the exit code and the tail of its output.
func (s *Service) CompleteShell(ctx context.Context, e ShellExit) (Result, error) {
	kind := "completed"
	if e.Code != 0 {
		kind = "failed"
	}
	text := fmt.Sprintf("shell:%s %s with exit code %d", e.ID, kind, e.Code)
	if e.Description != "" {
		text += ": " + e.Description
	}
	if tail := strings.TrimRight(e.Tail, "\n"); tail != "" {
		text += "\nLast output:\n```\n" + tail + "\n```"
	}
	text += "\nOutput: dyna shell tail " + e.ID
	if e.RoomID == "" {
		return Result{}, errNoRoom
	}
	rec, err := s.Rooms.Resolve(e.RoomID)
	if err != nil {
		return Result{}, err
	}
	return s.complete(ctx, e.RoomID, "shell:"+e.ID, s.parentTargets(rec.Name, e.Parent), kind, text, 0)
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
