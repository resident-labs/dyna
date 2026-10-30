// Package room stores Dyna rooms: one durable feed and namespace per related
// body of work.
//
// Every member and the owner can read every post in a room. Mentions decide
// who wakes, not who can read. The room name alone is the owner's address;
// <room>:<agent> addresses one member session.
package room

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/lockrank"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const (
	ArchiveAfter     = time.Hour
	MaxMessageBytes  = 16 * 1024
	maxRecordBytes   = 64 * 1024
	roomDirPrefix    = "room_"
	defaultPollEvery = 200 * time.Millisecond
)

var (
	ErrNotFound     = errors.New("no such room")
	ErrPostNotFound = errors.New("no such room post")
	nameRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	agentRe         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	idRe            = regexp.MustCompile(`^room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$`)
)

// Record is one room's metadata.
type Record struct {
	MergeMaxParallel int          `json:"mergeMaxParallel,omitempty"`
	GitHubAccess     GitHubAccess `json:"githubAccess,omitempty"`
	// ApprovalWait and ApprovalExpiry are this room's held-write wait and expiry.
	// Zero uses the defaults in internal/approvals.
	ApprovalWait   time.Duration `json:"approvalWait,omitempty"`
	ApprovalExpiry time.Duration `json:"approvalExpiry,omitempty"`
	RootProfile    string        `json:"rootProfile,omitempty"`
	// Upstream is the parent repository, as host/owner/repo, of this room's
	// fork checkout (see ParseUpstream). Empty means the room has no parent.
	Upstream string `json:"upstream,omitempty"`
	// Attribution is the line that agent-created pull request and issue bodies
	// end with. Empty means the room sets none.
	Attribution          string               `json:"attribution,omitempty"`
	AttributionPlacement AttributionPlacement `json:"attributionPlacement,omitempty"`
	// LibraryPinSet is true when this room has its own librarian profile.
	// LibraryPin is that profile; empty means auto for this room only.
	// When LibraryPinSet is false, the room uses the global library.profile.
	LibraryPinSet bool         `json:"libraryPinSet,omitempty"`
	LibraryPin    string       `json:"libraryPin,omitempty"`
	SessionPause  SessionPause `json:"sessionPause,omitempty"`
	// CheckIn is this room's parent check-in cadence (see CheckIn). The zero
	// value inherits the parent check-ins hook's global interval.
	CheckIn CheckIn `json:"checkIn,omitempty"`
	// Limits are this room's resource slice limits (see Limits). The zero
	// value uses the machine defaults.
	Limits Limits `json:"limits,omitempty"`
	// Seat is the row's seat column, which only writeSeat writes.
	Seat          Seat   `json:"-"`
	DisplayName   string `json:"displayName,omitempty"`
	ManualArchive bool   `json:"manualArchive,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
	PinOrder      int    `json:"pinOrder,omitempty"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Repo          string `json:"repo,omitempty"`
	// ExtraRepos lists more repositories, as main worktree paths, whose
	// checkouts the room accepts for local changes (see AddRepo).
	ExtraRepos []string `json:"extraRepos,omitempty"`
	// RepoUpstreams maps a registered extra repository's main worktree path to
	// its parent, as host/owner/repo (see ParseUpstream). The room's own
	// repository keeps its parent in Upstream (see UpstreamOf).
	RepoUpstreams map[string]string `json:"repoUpstreams,omitempty"`
	CreatedBy     string            `json:"createdBy,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	LastActivity  time.Time         `json:"lastActivity"`
}

// Archived reports whether this room is dormant at now. Live work keeps a
// room active regardless of message age.
func (r Record) Archived(now time.Time, hasLiveWork bool) bool {
	if hasLiveWork {
		return false
	}
	return r.ManualArchive || !now.Before(r.LastActivity.Add(ArchiveAfter))
}

// EventNotice names the registered type of a system event post. The facts of
// the event are in its typed payload (see Message.PR, Message.Hook and
// Message.Worktree). Older stored posts carry the same field name with the
// extra facts that now live in the payload; a reader ignores those.
type EventNotice struct {
	Kind   string `json:"kind"`
	Thread string `json:"thread,omitempty"`
	// Producer names the engine service that observed the event, such as the
	// parent check-in scheduler. It is data, never a sender: the post's sender
	// is the engine.
	Producer EventProducer `json:"producer,omitempty"`
}

// Message is one durable post. It is append-only: once written it never
// changes. To holds the full resolved recipient addresses (the room name is
// the owner); Via holds the mention tokens as the sender typed them.
type Message struct {
	// Wake asks the engine to start or steer the addressed sessions.
	Wake bool `json:"wake"`
	// SessionIDs binds participant addresses to exact sessions despite name reuse.
	SessionIDs map[string]string `json:"sessionIds,omitempty"`
	ID         string            `json:"id"`
	TS         int64             `json:"ts"`
	RoomID     string            `json:"roomId"`
	From       string            `json:"from"`
	// SenderRoomID is the room of the session that wrote the post, captured
	// from its stored session. Wake policy compares rooms by this identity,
	// never by the sender's display address.
	SenderRoomID string      `json:"senderRoomId,omitempty"`
	Kind         string      `json:"kind,omitempty"`
	Notice       EventNotice `json:"notice,omitzero"`
	// Payload is the typed JSON of a sealed system event. Only PostEvent
	// builds one; read it with the event accessor, such as Worktree().
	Payload EventPayload `json:"payload,omitzero"`
	// Human marks an owner post a person typed in the desktop composer.
	Human bool `json:"human,omitempty"`
	// Script names the workflow run that wrote the post. The feed shows the
	// workflow's name and links to the run, not the run's address.
	Script *ScriptRef `json:"script,omitempty"`
	// Conversation is where the post was written: the room feed, one
	// session's DM or the owner's voice call. The store decides it once, when
	// the post is appended (see HomeFor), and never changes it. The empty
	// value reads as the room feed.
	Conversation Conversation `json:"conversation,omitempty"`
	// Source is SourceVoice on posts of the owner's voice call and SourceCard on text an HTML card wrote.
	Source string `json:"source,omitempty"`
	// Spoken marks a transcript line of the voice call.
	Spoken bool `json:"spoken,omitempty"`
	// RelayOf is the post whose answer Voice relays to the owner.
	RelayOf string   `json:"relayOf,omitempty"`
	To      []string `json:"to"`
	Via     []string `json:"via,omitempty"`
	ReplyTo string   `json:"replyTo,omitempty"`
	// Forward is set when this post forwards another post. It holds a
	// snapshot of the original, so every reader sees it without a fetch.
	Forward *Forward `json:"forward,omitempty"`
	// Turn is set on session completion records: the turn that finished.
	Turn int `json:"turn,omitempty"`
	// OwnerReport marks a completion owed to the room owner, such as a
	// top-level session's report. Other completions reach the owner only
	// for the desktop. See RootContext.
	OwnerReport bool `json:"ownerReport,omitempty"`
	// NeedsRoot marks a post that requires the root seat to act.
	NeedsRoot bool       `json:"needsRoot,omitempty"`
	Text      string     `json:"text"`
	Skill     *SkillRef  `json:"skill,omitempty"`
	Prompt    *PromptRef `json:"mcpPrompt,omitempty"`
	// Attachments are files saved with the room before this post.
	Attachments []Attachment `json:"attachments,omitempty"`
	References  []Reference  `json:"references,omitempty"`
}

// SkillRef identifies the skill chosen for this message and its instruction file.
type SkillRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// PromptRef identifies the MCP server and prompt chosen for a message.
type PromptRef struct {
	Server string `json:"server"`
	Prompt string `json:"prompt"`
}

// ScriptRef is the workflow run behind a script's post, stored on the post so
// the feed needs no lookup to name the sender.
type ScriptRef struct {
	RunID string `json:"runId"`
	// Workflow is the run's name, as the Runs page shows it.
	Workflow string `json:"workflow"`
}

// OwnerLabel names the room owner as a speaker. Root and the human both use
// the room's name as their address; the human flag tells them apart.
func OwnerLabel(human bool) string {
	if human {
		return "you"
	}
	return "root"
}

// QuestionID reads the marker on an ordinary question post.
func (m Message) QuestionID() string {
	if m.Kind != "message" {
		return ""
	}
	match := questionMarker.FindStringSubmatch(m.Text)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

var questionMarker = regexp.MustCompile(`^\[QUESTION (q\d+)\]`)

// Forward records the post that a forwarded post carries. Posts never change,
// so the snapshot always matches the original. Attachments are references to
// files already saved with the room; forwarding does not copy any file.
type Forward struct {
	ID          string       `json:"id"`
	RoomID      string       `json:"roomId"`
	TS          int64        `json:"ts"`
	From        string       `json:"from"`
	Kind        string       `json:"kind,omitempty"`
	Notice      EventNotice  `json:"notice,omitzero"`
	Human       bool         `json:"human,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	References  []Reference  `json:"references,omitempty"`
}

// NewForward snapshots a stored post for forwarding.
func NewForward(m Message) *Forward {
	return &Forward{ID: m.ID, RoomID: m.RoomID, TS: m.TS, From: m.From, Kind: m.Kind, Notice: m.Notice, Human: m.Human, Text: m.Text, Attachments: m.Attachments, References: append([]Reference(nil), m.References...)}
}

// Block renders the forwarded post as quoted text with a line that says who
// wrote it and when.
func (f Forward) Block() string {
	from := f.From
	if f.Human {
		from += " (human)"
	}
	at := time.UnixMilli(f.TS).Local().Format("2006-01-02 15:04 MST")
	var b strings.Builder
	fmt.Fprintf(&b, "[Forwarded post %s, originally written by %s at %s]", f.ID, from, at)
	if body := referenceText(f.Text, f.References); body != "" {
		for _, line := range strings.Split(body, "\n") {
			b.WriteString("\n> " + line)
		}
	}
	b.WriteString("\n[End of forwarded post]")
	return b.String()
}

// flat returns the post as readers outside the desktop see it: the note and
// the quoted original as text, and both attachment lists as one.
func (m Message) flat() Message {
	m.Text = referenceText(m.Text, m.References)
	m.References = nil
	if m.Forward == nil {
		return m
	}
	block := m.Forward.Block()
	if m.Text != "" {
		m.Text += "\n\n" + block
	} else {
		m.Text = block
	}
	m.Attachments = append(append([]Attachment(nil), m.Attachments...), m.Forward.Attachments...)
	m.Forward = nil
	return m
}

// Addressed reports whether addr is one of the post's recipients.
func (m Message) Addressed(addr string) bool {
	for _, to := range m.To {
		if to == addr {
			return true
		}
	}
	return false
}

// SessionAddressed reports whether post m names session id, which is currently
// named addr. A recipient bound to the session by ID keeps matching after a
// rename. An unbound recipient matches its address text.
//
// Who a post reached is no longer read from this: a session's input and its
// wakes come from its delivery rows (see DeliveriesFor and SessionDeliveries).
// This answers what the post names, which decides what a turn owes, not what it
// was delivered.
func (m Message) SessionAddressed(id, addr string) bool {
	for _, to := range m.To {
		if m.IsSession(to, id, addr) {
			return true
		}
	}
	return false
}

// AuthorSessionID is the sender's session ID when SessionIDs binds the
// sender's address, and empty for an owner, engine or worker post. The
// binding survives a rename, so the DM of a renamed session keeps its rows.
func (m Message) AuthorSessionID() string { return m.SessionIDs[m.From] }

// SenderKind names what kind of author wrote a post.
type SenderKind string

const (
	// SenderEngine is the engine's own address.
	SenderEngine SenderKind = "engine"
	// SenderOwner is the human owner, or a post written as the room itself.
	SenderOwner SenderKind = "owner"
	// SenderRoot is a room's root seat.
	SenderRoot SenderKind = "root"
	// SenderSession is any other agent session of a room, bound to its
	// session ID in SessionIDs.
	SenderSession SenderKind = "session"
	// SenderUnbound is a room:agent address that SessionIDs does not bind.
	SenderUnbound SenderKind = "unbound"
	// SenderShell is a Dyna-managed shell.
	SenderShell SenderKind = "shell"
	// SenderRun is a workflow run.
	SenderRun SenderKind = "run"
)

// Sender parses the post's author from fields every post already has. The
// shell: and run: address prefixes are read here and nowhere else.
func (m Message) Sender() SenderKind {
	if m.Human {
		return SenderOwner
	}
	switch {
	case m.From == EngineSender:
		return SenderEngine
	case strings.HasPrefix(m.From, "shell:"):
		return SenderShell
	case strings.HasPrefix(m.From, "run:"):
		return SenderRun
	}
	if _, agent, ok := strings.Cut(m.From, ":"); ok && agent != "" {
		if agent == RootAgent {
			return SenderRoot
		}
		if m.AuthorSessionID() != "" {
			return SenderSession
		}
		return SenderUnbound
	}
	return SenderOwner
}

// SentBySession reports whether post m was written by session id, currently
// named addr. It follows the same binding rule as SessionAddressed.
func (m Message) SentBySession(id, addr string) bool {
	return m.IsSession(m.From, id, addr)
}

// IsSession checks one address key. A key bound in SessionIDs names exactly
// one session. An unbound key matches the session's current address text.
func (m Message) IsSession(key, id, addr string) bool {
	if bound := m.SessionIDs[key]; bound != "" {
		return bound == id
	}
	return key == addr
}

// Deliverable distinguishes agent input from informational room notices. A
// registered event follows its spec's quiet flag.
func (m Message) Deliverable() bool {
	if spec, ok := eventSpecFor(m.Kind, m.Notice.Kind); ok {
		return !spec.Quiet
	}
	// TODO: legacy/shim remove later — a post stored before its family was
	// typed is a notice by its old kind. Remove when retained history ages out.
	return m.Kind != "notice"
}

// maxToShown bounds the recipients listed in an envelope's To line so the
// header stays small enough for live steering.
const maxToShown = 8

// FrameOptions says who reads a frame and how to reply.
type FrameOptions struct {
	// Room is the room name (used for the owner label and pointer command).
	Room string
	// Reader is the recipient's own address; it is marked "(you)".
	Reader string
	// Reply is the address a reply should mention. Empty omits the line.
	Reply string
	// ReplyTo includes the original post ID when the reply must be linked.
	ReplyTo string
	// FilesDir is the room's attachment directory on this host. Empty lists
	// attachments by retrieval command instead of path.
	FilesDir string
	// Sent is when the post was stored. Delivered is when this turn took it.
	// Input is "waking" or "quiet". Empty omits those lines, so an old frame
	// stays unchanged.
	Sent      time.Time
	Delivered time.Time
	Input     string
}

// Named returns the recipients the sender named one by one, without the
// leading @ and without selectors such as @team or @children. recorded is
// false when the post kept no mentions.
func (m Message) Named() (refs []string, recorded bool) {
	for _, tok := range m.Via {
		if !selectorToken(tok) {
			refs = append(refs, strings.TrimPrefix(tok, "@"))
		}
	}
	return refs, len(m.Via) > 0
}

// selectorToken reports whether a mention token is a selector rather than a
// single named recipient.
func selectorToken(tok string) bool {
	local := strings.TrimPrefix(tok, "@")
	if i := strings.LastIndex(local, ":"); i >= 0 && !strings.HasPrefix(local, "run:") {
		local = local[i+1:]
	}
	switch local {
	case "root", "team", "parent", "siblings", "children":
		return true
	}
	return false
}

// Header renders the envelope header lines: the message line, To, and Reply.
func (m Message) Header(o FrameOptions) string {
	var b strings.Builder
	from := m.From
	if m.Human {
		from += " (human)"
	}
	fmt.Fprintf(&b, "[dyna message %s from %s]\n", m.ID, from)
	shown := m.To
	extra := 0
	if len(shown) > maxToShown {
		extra = len(shown) - maxToShown
		shown = shown[:maxToShown]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, to := range shown {
		label := to
		if to == o.Room {
			label = to + " (root)"
		}
		if to == o.Reader {
			label += " (you)"
		}
		parts = append(parts, label)
	}
	if extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", extra))
	}
	var groups []string
	for _, tok := range m.Via {
		if selectorToken(tok) {
			groups = append(groups, tok)
		}
	}
	b.WriteString("To: ")
	if len(groups) > 0 {
		b.WriteString(strings.Join(m.Via, " ") + " = ")
	}
	b.WriteString(strings.Join(parts, ", "))
	if o.Reply != "" {
		fmt.Fprintf(&b, "\nReply: dyna post @%s", o.Reply)
		if o.ReplyTo != "" {
			fmt.Fprintf(&b, " --reply-to %s", o.ReplyTo)
		}
		b.WriteString(" \"…\"")
		if len(groups) > 0 && o.ReplyTo == "" {
			fmt.Fprintf(&b, "   (to all: add %s)", strings.Join(groups, " "))
		}
	}
	if o.Input != "" {
		if !o.Sent.IsZero() {
			fmt.Fprintf(&b, "\nSent: %s", o.Sent.Local().Format("2006-01-02 15:04:05"))
		}
		if !o.Delivered.IsZero() {
			fmt.Fprintf(&b, "\nDelivered: %s", o.Delivered.Local().Format("2006-01-02 15:04:05"))
		}
		fmt.Fprintf(&b, "\nInput: %s", o.Input)
	}
	return b.String()
}

// Frame renders the whole post as it is delivered into a follow-up turn or a
// drained turn prompt.
func (m Message) Frame(o FrameOptions) string {
	m = m.flat()
	out := m.Header(o)
	if m.Text != "" {
		out += "\n\n" + m.Text
	}
	if m.Skill != nil {
		out += "\n\nUse the " + m.Skill.Name + " skill: read " + m.Skill.Path + " for this message."
	}
	if man := m.manifest(o, 0); man != "" {
		out += "\n\n" + man
	}
	return out
}

// SteeringFrame renders the post for live steering, which has a hard byte
// limit on the whole steered string. A post that does not fit keeps its
// header, shows the longest text prefix that fits, and points to the stored
// post. It reports whether it truncated.
func (m Message) SteeringFrame(o FrameOptions, limit int) (string, bool) {
	m = m.flat()
	if full := m.Frame(o); len(full) <= limit {
		return full, false
	}
	header := m.Header(o)
	if m.Skill != nil {
		header += "\n\nUse the " + m.Skill.Name + " skill: read " + m.Skill.Path + " for this message."
	}
	total := len(m.Text)
	trailer := func(shown int) string {
		return fmt.Sprintf("[Truncated: %d of %d bytes. Full text: dyna room read %s %s]", shown, total, o.Room, m.ID)
	}
	const ellipsis = "…\n"
	// The attachment list outlives a long caption: it may take up to half
	// the limit, never more than what the header and trailer leave, and the
	// caption is cut first. When even a short list does not fit, one line
	// points to the stored post.
	man := ""
	if len(m.Attachments) > 0 {
		room := limit - len(header) - len("\n\n") - len(ellipsis) - len(trailer(total)) - len("\n\n")
		man = m.manifest(o, min(limit/2, room))
		if len(man) > min(limit/2, room) {
			man = fmt.Sprintf("Attachments (%d): dyna room read %s %s", len(m.Attachments), o.Room, m.ID)
		}
		man = "\n\n" + man
	}
	budget := limit - len(header) - len("\n\n") - len(ellipsis) - len(trailer(total)) - len(man)
	if budget < 0 {
		budget = 0
	}
	prefix := TruncateUTF8(m.Text, budget)
	return header + "\n\n" + prefix + ellipsis + trailer(len(prefix)) + man, true
}

// TruncateUTF8 returns the longest prefix of s of at most n bytes that ends
// on a UTF-8 boundary.
func TruncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut]
}

// Address is a parsed Dyna message address.
type Address struct {
	Room  string
	Agent string
}

func (a Address) String() string {
	if a.Agent == "" {
		return a.Room
	}
	return a.Room + ":" + a.Agent
}

func (a Address) IsRoom() bool { return a.Agent == "" }

// ParseAddress parses a session address "<room>:<agent>" (or a bare room name).
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Address{}, errors.New("a room or agent address is required")
	}
	name, agent, hasAgent := strings.Cut(s, ":")
	if err := ValidateName(name); err != nil {
		return Address{}, err
	}
	if hasAgent {
		if err := ValidateAgent(agent); err != nil {
			return Address{}, err
		}
		if strings.Contains(agent, ":") {
			return Address{}, fmt.Errorf("invalid address %q", s)
		}
	}
	return Address{Room: name, Agent: agent}, nil
}

func ValidateName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "--") || ReservedName(name) {
		return fmt.Errorf("invalid room name %q", name)
	}
	return nil
}

func ValidateAgent(name string) error {
	if !agentRe.MatchString(name) || strings.HasPrefix(name, runstore.SessionDirPrefix) {
		return fmt.Errorf("invalid agent name %q", name)
	}
	if ReservedName(name) {
		return fmt.Errorf("agent name %q is reserved for room mentions", name)
	}
	return nil
}

// ReservedName reports whether name is a mention selector, or the removed
// @room or @owner. No agent may use one as its name.
func ReservedName(name string) bool {
	switch name {
	case RootAgent, VoiceAgent, "team", "parent", "siblings", "children", "room", "owner":
		return true
	}
	return false
}

// TODO: legacy/shim remove later — keeps the removed "global" room readable (its dir and roomId "global" in old session and run metadata); remove after the owner deletes the global room.
const legacyGlobalID = "global"

func ValidID(id string) bool { return id == legacyGlobalID || idRe.MatchString(id) }

// Store addresses the rooms directory.
type Store struct {
	Root          string
	Now           func() time.Time
	decodeMessage func([]byte, *Message) error
	logMu         sync.Mutex
	logs          map[string]*messageLog
}

func DefaultStore() *Store { return &Store{Root: filepath.Join(store.DataDir(), "rooms")} }

func (s *Store) root() string {
	if s != nil && s.Root != "" {
		return s.Root
	}
	return filepath.Join(store.DataDir(), "rooms")
}

func (s *Store) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) dir(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid room id %q", id)
	}
	return filepath.Join(s.root(), id), nil
}

func (s *Store) messagesPath(id string) (string, error) {
	dir, err := s.dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "messages.jsonl"), nil
}

func (s *Store) withRootLock(fn func() error) error {
	if err := os.MkdirAll(s.root(), 0o755); err != nil {
		return err
	}
	defer lockrank.Acquire(lockrank.RoomRoot)()
	unlock, err := store.Lock(filepath.Join(s.root(), ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// withRoomLock runs fn under the room lock. No caller passes a deadline yet;
// engine calls pass theirs when the CLI writes move to the engine (PR 4).
func (s *Store) withRoomLock(id string, fn func() error) error {
	return s.lockRoom(context.Background(), id, fn)
}

// lockRoom runs fn under room.lock, a flock that orders the room's writers
// across processes, waiting no longer than ctx.
func (s *Store) lockRoom(ctx context.Context, id string, fn func() error) error {
	dir, err := s.dir(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w %q", ErrNotFound, id)
		}
		return err
	}
	defer lockrank.Acquire(lockrank.Room)()
	unlock, err := store.LockContext(ctx, filepath.Join(dir, "room.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func newID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s%s-%s", roomDirPrefix, now.Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// CreateInput names the room to create or reuse. Repo is the canonical
// main worktree path when the room belongs to a git repo, else empty.
// An empty Name derives a name from Repo only when no room exists for it.
type CreateInput struct {
	GitHubAccess GitHubAccess
	Name         string
	Repo         string
	CreatedBy    string
	// RootProfile applies only to new rooms. Empty inherits root.profile.
	RootProfile string
}

// Create returns the room for in.Repo, or for in.Name without a repo. A
// matching room is reused regardless of the requested name and its activity
// is touched, which revives an archived room. Without a matching repo, a
// name held by a different repo or an explicit room is an error.
func (s *Store) Create(in CreateInput) (Record, error) {
	if in.GitHubAccess == "" {
		in.GitHubAccess = GitHubReadOnly
	}
	if err := ValidateGitHubAccess(in.GitHubAccess); err != nil {
		return Record{}, err
	}
	if in.Repo != "" {
		if root, err := RepoRoot(in.Repo); err == nil {
			in.Repo = root
		} else if !errors.Is(err, ErrNotRepo) && !os.IsNotExist(err) {
			return Record{}, err
		}
	}
	name := strings.TrimSpace(in.Name)
	var out Record
	err := s.withRootLock(func() error {
		existing, err := s.listRecordsLocked()
		if err != nil {
			return err
		}
		// Prefer the repository match even if the requested name is held
		// by another room. The repository has one room under every name.
		match := -1
		for i, rec := range existing {
			if in.Repo != "" && rec.Repo == in.Repo {
				match = i
				break
			}
		}
		if match == -1 {
			if name == "" && in.Repo != "" {
				name, err = RepoRoomName(in.Repo)
				if err != nil {
					return err
				}
			}
			// A name that looks like a room ID would be shadowed by that ID.
			if ValidID(name) {
				return fmt.Errorf("room name %q is reserved", name)
			}
			if err := ValidateName(name); err != nil {
				return err
			}
			for i, rec := range existing {
				if rec.Name != name {
					continue
				}
				if rec.Repo != in.Repo {
					return nameConflict(rec, in.Repo)
				}
				match = i
				break
			}
		}
		if match != -1 {
			out = existing[match]
			if err := s.touchRecord(out); err != nil {
				return err
			}
			out, err = s.readRecord(out.ID)
			return err
		}
		now := s.now()
		out = Record{
			ID: newID(now), Name: name, Repo: in.Repo, CreatedBy: strings.TrimSpace(in.CreatedBy),
			GitHubAccess: in.GitHubAccess,
			CreatedAt:    now, LastActivity: now,
			RootProfile: strings.TrimSpace(in.RootProfile),
		}
		return s.createRecordLocked(out)
	})
	return out, err
}

func nameConflict(rec Record, repo string) error {
	held := "an explicit room (no repo)"
	if rec.Repo != "" {
		held = "repo " + rec.Repo
	}
	want := "an explicit room (no repo)"
	if repo != "" {
		want = "repo " + repo
	}
	return fmt.Errorf("room %q already belongs to %s, not %s; pass an explicit distinct name: dyna room create <name>", rec.Name, held, want)
}

func (s *Store) createRecordLocked(rec Record) error {
	dir, err := s.dir(rec.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := createLog(filepath.Join(dir, "messages.jsonl")); err != nil {
		return err
	}
	return s.writeRecord(rec)
}

// createLog creates a new room's empty message log. It is its own function
// so the write ratchet can name it as not-state.
func createLog(path string) error { return os.WriteFile(path, nil, 0o600) }

func (s *Store) listRecordsLocked() ([]Record, error) {
	db, err := s.recordsDB()
	if err != nil {
		return nil, err
	}
	out, err := storedRecords(db)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivity.After(out[j].LastActivity) })
	return out, nil
}

// Resolve finds a room by name or canonical ID. An empty reference is an
// error: no record may fall back to a default room.
func (s *Store) Resolve(ref string) (Record, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Record{}, errors.New("a room is required")
	}
	if ValidID(ref) {
		return s.readRecord(ref)
	}
	if err := ValidateName(ref); err != nil {
		return Record{}, err
	}
	records, err := s.listRecordsLocked()
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.Name == ref {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%w %q", ErrNotFound, ref)
}

// Delete removes a room's record and log for good. ref is a name or an ID.
// check runs under the store lock and may veto the delete; it sees the
// resolved record. Sessions, runs and shells that
// point at the room are not touched.
func (s *Store) Delete(ref string, check func(Record) error) (Record, error) {
	var rec Record
	err := s.withRootLock(func() error {
		var err error
		if rec, err = s.Resolve(ref); err != nil {
			return err
		}
		if check != nil {
			if err := check(rec); err != nil {
				return err
			}
		}
		dir, err := s.dir(rec.ID)
		if err != nil {
			return err
		}
		// The rows go first: if that fails, the room still resolves and the
		// deletion can be retried.
		if err := s.deleteRows(rec.ID); err != nil {
			return err
		}
		return removeDir(dir)
	})
	if err == nil {
		path, _ := s.messagesPath(rec.ID)
		s.logMu.Lock()
		s.dropMessageLog(path)
		s.logMu.Unlock()
	}
	return rec, err
}

// List returns rooms newest activity first. Archived rooms are hidden
// unless includeArchived is set.
func (s *Store) List(includeArchived bool) ([]Record, error) {
	return s.ListWithActivity(includeArchived, nil)
}

// ListWithActivity uses activity supplied by the application to keep busy rooms visible.
func (s *Store) ListWithActivity(includeArchived bool, live map[string]bool) ([]Record, error) {
	records, err := s.listRecordsLocked()
	if err != nil {
		return nil, err
	}
	s.pruneMessageLogs(records)
	if includeArchived {
		return records, nil
	}
	now := s.now()
	out := records[:0]
	for _, rec := range records {
		if !rec.Archived(now, live[rec.ID]) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Touch records activity without posting a message.
func (s *Store) Touch(roomID string) error {
	return s.withRoomLock(roomID, func() error {
		rec, err := s.readRecord(roomID)
		if err != nil {
			return err
		}
		return s.touchRecord(rec)
	})
}

// PostInput is one post to append. The store stamps ID, time, and room.
type PostInput struct {
	Wake       bool
	SessionIDs map[string]string
	// SenderRoomID is the room of the sending session, from its stored
	// session. Empty for the owner, the engine, a shell or a workflow run.
	SenderRoomID string
	// ID is an optional stable msg ID for retrying integrations. Reusing it
	// requires the same sender, recipients and content. Ordinary posts leave it empty.
	ID   string
	From string
	Kind string
	// Notice names the registered type of a system event post.
	Notice EventNotice
	// payload is the typed JSON of a sealed event. Only PostEvent sets it.
	payload EventPayload
	Human   bool
	// Script is the workflow run that wrote the post, for a script's post().
	Script  *ScriptRef
	To      []string
	Via     []string
	ReplyTo string
	// Conversation is the conversation the writer says it wrote in. Empty
	// lets the store decide where the post belongs (see HomeFor).
	Conversation Conversation
	Forward      *Forward
	Turn         int
	OwnerReport  bool
	NeedsRoot    bool
	Text         string
	Skill        *SkillRef
	Prompt       *PromptRef
	Source       string
	Spoken       bool
	RelayOf      string
	// Attachments must already be saved in this room (PutAttachment).
	Attachments []Attachment
	References  []Reference
}

// stripSelfLabel drops a leading "name:" that repeats the sender, as in
// "lead: done". The feed already shows who sent a post, and models add the
// label out of chat habit, so a prompt rule would only lower the odds. It
// matches the sender's member name or full address, plain or in bold, and
// keeps the text when nothing would be left.
func stripSelfLabel(from, text string) string {
	names := []string{from}
	if i := strings.LastIndex(from, ":"); i >= 0 {
		names = append(names, from[i+1:])
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, label := range []string{name + ":", "**" + name + ":**", "**" + name + "**:"} {
			if len(text) < len(label) || !strings.EqualFold(text[:len(label)], label) {
				continue
			}
			rest := text[len(label):]
			if rest != "" && rest[0] != ' ' && rest[0] != '\n' && rest[0] != '\t' {
				continue
			}
			if rest = strings.TrimSpace(rest); rest != "" {
				return rest
			}
		}
	}
	return text
}

func newMessageID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "msg_" + hex.EncodeToString(b[:])
}

// messageIDShape is the form of a stored post ID: msg_ and eight random bytes
// in hex. A caller that names its own ID must match it.
var messageIDShape = regexp.MustCompile(`^msg_[a-f0-9]{16}$`)

// IsMessageID reports whether id has the form of a stored post ID, so a caller
// can tell an ID from text that merely starts with msg_.
func IsMessageID(id string) bool { return messageIDShape.MatchString(id) }

// ErrPostIDConflict means a post ID is stored with other content. A caller
// that derives the ID from what it reports reads it as "already posted".
var ErrPostIDConflict = errors.New("post ID already belongs to different content")

// Post appends a message and updates the room activity time.
func (s *Store) Post(roomID string, in PostInput) (Message, error) {
	return s.post(roomID, in, false)
}

// postConflicts reports whether a retry of in differs from the stored post m
// with the same ID. The conversation of a post is decided when it is stored,
// so it only counts when the retry names one itself: a retry of a post stored
// before the field existed names none and matches the home it resolves to.
func postConflicts(m Message, in PostInput, supplied Conversation) bool {
	if m.NeedsRoot != in.NeedsRoot || m.Wake != in.Wake || m.From != in.From || m.SenderRoomID != in.SenderRoomID || m.Kind != in.Kind || m.Notice != in.Notice || !bytes.Equal(m.Payload.raw, in.payload.raw) || m.Human != in.Human || m.Source != in.Source || m.Spoken != in.Spoken || m.RelayOf != in.RelayOf || m.Text != in.Text || m.ReplyTo != in.ReplyTo || !reflect.DeepEqual(m.Prompt, in.Prompt) || !reflect.DeepEqual(m.References, in.References) || !reflect.DeepEqual(m.Forward, in.Forward) || m.Turn != in.Turn || !reflect.DeepEqual(m.To, in.To) || !reflect.DeepEqual(m.SessionIDs, in.SessionIDs) || !reflect.DeepEqual(unversioned(m.Attachments), unversioned(in.Attachments)) {
		return true
	}
	return supplied != "" && m.Conversation.Home() != supplied.Home()
}

// post appends a message. event lets PostEvent write a sealed family, which
// no other caller can do.
func (s *Store) post(roomID string, in PostInput, event bool) (Message, error) {
	if in.ID != "" && !IsMessageID(in.ID) {
		return Message{}, fmt.Errorf("invalid post ID")
	}
	in.From = strings.TrimSpace(in.From)
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Prompt == nil {
		in.Text = strings.TrimSpace(in.Text)
	}
	if in.From == "" || len(in.To) == 0 {
		return Message{}, errors.New("a post needs a sender and at least one recipient")
	}
	if in.Text == "" && len(in.Attachments) == 0 && len(in.References) == 0 && in.Forward == nil {
		return Message{}, errors.New("message text is required")
	}
	if !utf8.ValidString(in.Text) {
		return Message{}, errors.New("message text must be valid UTF-8")
	}
	if len(in.Text) > MaxMessageBytes {
		return Message{}, fmt.Errorf("message is too long (maximum %s)", settings.FormatBytes(int64(MaxMessageBytes)))
	}
	if in.Kind == "" {
		in.Kind = "message"
	}
	if !event {
		if sealedFamily(in.Kind) {
			return Message{}, fmt.Errorf("%q is a sealed event family: post it with PostEvent", in.Kind)
		}
		if !agentKind(in.Kind) && !voiceCallPost(in.Kind) {
			return Message{}, fmt.Errorf("post kind %q is not registered in internal/room/events.go", in.Kind)
		}
	}
	if in.Kind == "message" && in.Prompt == nil {
		in.Text = stripSelfLabel(in.From, in.Text)
	}
	if err := in.Conversation.Validate(); err != nil {
		return Message{}, err
	}

	if err := ValidateReferences(in.References); err != nil {
		return Message{}, err
	}
	in.References = append([]Reference(nil), in.References...)
	for i, ref := range in.References {
		// JSON decodes numbers as float64. Canonicalize Go callers too, so
		// retry comparisons do not depend on whether the input came from JSON.
		if n, ok := ref.Number.(int); ok {
			in.References[i].Number = float64(n)
		}
	}
	refs := issueReferences{store: s}
	if err := refs.canonical(roomID, in.References); err != nil {
		return Message{}, err
	}
	if len(in.Attachments) > 0 {
		atts, err := s.canonAttachments(roomID, in.Attachments)
		if err != nil {
			return Message{}, err
		}
		in.Attachments = atts
		for _, a := range atts {
			// Text readers (agents, `dyna room read`) never see the card, so the
			// text is what they get instead.
			if a.Rich() && in.Text == "" {
				return Message{}, errors.New("a post with an HTML card needs text that sums it up for readers outside the desktop")
			}
		}
	}

	var out Message
	err := s.withRoomLock(roomID, func() error {
		rec, err := s.readRecord(roomID)
		if err != nil {
			return err
		}
		var msgs []Message
		if in.ID != "" || updatesVisual(in.Attachments) {
			if msgs, err = s.Messages(roomID); err != nil {
				return err
			}
		}
		// Stamp first, so a retry compares the resolved visual, not the
		// alias the caller named.
		if err := stampVersions(msgs, in.Attachments); err != nil {
			return err
		}
		// Resolve the home before the retry check, so a retry compares the
		// conversation the post is stored in, not the one the caller named. A
		// process that may not migrate dyna.db (such as dyna doctor) stores the
		// post without rows, and the next engine start fills them.
		indexDB, indexErr := s.postIndexDB()
		if indexErr != nil {
			log.Printf("room %s: post index is not ready, the next engine start fills this post's rows: %v", roomID, indexErr)
			indexDB = nil
		}
		parent := s.ReplyHome(indexDB, roomID, in.ReplyTo)
		supplied := in.Conversation
		// The reader of the home needs the notice kind and the room too: a typed
		// event's audience decides whether its post is a DM, and the seat's DM
		// names the room.
		in.Conversation = HomeFor(Message{RoomID: roomID, From: in.From, Kind: in.Kind, Notice: in.Notice, Human: in.Human, Source: in.Source, To: in.To, SessionIDs: in.SessionIDs, Conversation: in.Conversation}, rec.Name, parent)
		if in.ID != "" {
			for _, m := range msgs {
				if m.ID == in.ID {
					if postConflicts(m, in, supplied) {
						return ErrPostIDConflict
					}
					out = m
					return nil
				}
			}
		}
		now := s.now()
		out = Message{
			Wake: in.Wake, ID: newMessageID(), TS: now.UnixMilli(), RoomID: roomID, SessionIDs: in.SessionIDs, SenderRoomID: in.SenderRoomID,
			From: in.From, Kind: in.Kind, Notice: in.Notice, Payload: in.payload, Human: in.Human, Script: in.Script, Conversation: in.Conversation, Source: in.Source, Spoken: in.Spoken, RelayOf: in.RelayOf, To: in.To, Via: in.Via, ReplyTo: in.ReplyTo, Forward: in.Forward, Turn: in.Turn, OwnerReport: in.OwnerReport, NeedsRoot: in.NeedsRoot, Text: in.Text, Skill: in.Skill, Prompt: in.Prompt,
			Attachments: in.Attachments, References: append([]Reference(nil), in.References...),
		}
		if in.ID != "" {
			out.ID = in.ID
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		path, err := s.messagesPath(roomID)
		if err != nil {
			return err
		}
		db, err := database.Open(filepath.Dir(s.root()))
		if err != nil {
			return err
		}
		// The append has no deadline today; the room lock above orders posts.
		logStart, err := s.FileSize(roomID)
		if err != nil {
			return err
		}
		// Every post gets its index rows here, whoever posted it, so no caller
		// has to remember them. The rows go in first, so a reader that finds
		// this post in the log also finds the rows that decide who reads it. A
		// failed row write does not fail the stored post: the log holds it, and
		// the cursor catch-up fills the rows at the next engine start. A failed
		// append takes the new rows out again, so no delivery outlives its post.
		rowsErr := s.indexPostRows(roomID, rec.Name, out)
		if err = state.For(db).Append(context.Background(), hub.Event{Topic: "room-post", Room: roomID}, path, b); err != nil {
			if rowsErr == nil {
				s.dropPostRows(roomID, out.ID)
			}
			return err
		}
		if rowsErr != nil {
			log.Printf("room %s: post %s: post index rows not written, the cursor catch-up will fill them: %v", roomID, out.ID, rowsErr)
		} else if err = s.advancePostIndex(roomID, logStart); err != nil {
			log.Printf("room %s: post %s: post index cursor not moved, the cursor catch-up will check it: %v", roomID, out.ID, err)
		}
		return s.touchRecord(rec)
	})
	return out, err
}

// Messages returns every retained message in append order.
func (s *Store) Messages(roomID string) ([]Message, error) {
	msgs, _, err := s.ReadAll(roomID, 0)
	return msgs, err
}

// ReadAll returns every complete message after byte offset and the next
// offset. ReadFrom stops after 4 MiB, so a large log takes several reads.
func (s *Store) ReadAll(roomID string, offset int64) ([]Message, int64, error) {
	var out []Message
	for {
		msgs, next, err := s.ReadFrom(roomID, offset)
		out = append(out, msgs...)
		if err != nil || next == offset {
			return out, next, err
		}
		offset = next
	}
}

// readFromUncached preserves reads that start inside a record rather than at
// an indexed line boundary. It uses the same decoder as the shared index.
func (s *Store) readFromUncached(roomID string, offset int64) ([]Message, int64, error) {
	path, err := s.messagesPath(roomID)
	if err != nil {
		return nil, offset, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			if _, err := s.readRecord(roomID); err != nil {
				return nil, offset, err
			}
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	var out []Message
	next, err := s.scanMessageLines(roomID, io.LimitReader(f, 4<<20), offset, func(line messageLine) {
		if line.msg.ID != "" {
			out = append(out, line.msg)
		}
	})
	return out, next, err
}

// FileSize returns the current append offset for a room message log.
func (s *Store) FileSize(roomID string) (int64, error) {
	path, err := s.messagesPath(roomID)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// OwnerReadIDs returns message IDs the owner has marked read in the desktop app for one room.
func (s *Store) OwnerReadIDs(roomID string) (map[string]bool, error) {
	db, err := s.ownerReadsDB()
	if err != nil {
		return nil, err
	}
	return storedOwnerReads(db, roomID)
}

// MarkOwnerRead records one post as read by the owner in the desktop app.
func (s *Store) MarkOwnerRead(roomID, messageID string) error {
	return s.MarkOwnerReadMany(roomID, []string{messageID})
}

// MarkOwnerReadMany records posts as read in one write.
func (s *Store) MarkOwnerReadMany(roomID string, messageIDs []string) error {
	return s.withRoomLock(roomID, func() error {
		db, err := s.ownerReadsDB()
		if err != nil {
			return err
		}
		if len(messageIDs) == 0 {
			return nil
		}
		return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
			_, err := putOwnerReads(context.Background(), conn, roomID, messageIDs)
			return err
		})
	})
}

// Message returns one stored post by ID from one room's log.
func (s *Store) Message(roomID, messageID string) (Message, error) {
	var out Message
	err := s.withMessageLog(roomID, func(log *messageLog) {
		if i, ok := log.byID[messageID]; ok {
			out = cloneMessage(log.lines[i].msg)
		}
	})
	if err != nil {
		return Message{}, err
	}
	if out.ID != "" {
		return out, nil
	}
	return Message{}, fmt.Errorf("%w %q in this room", ErrPostNotFound, messageID)
}
