// Package subagent implements long-lived, steerable, resumable worker sessions
// ("subagent mode"): a session owns one harness session that persists across
// turns. Ordinary sessions have no timeouts, idle expiry, or turn cap. They may
// start dyna workflows and subagents (bounded only by subagent.maxDepth).
//
// Sessions live next to runs: <data>/sessions/<ses_id>/. A turn is executed by
// a detached runner process that holds the session's turn.lock for as long as
// the turn lives, so "is the turn running" is a kernel fact, not a pid guess.
package subagent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/lockrank"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/turnlock"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workeroutput"
)

// Session lifecycle: queued/running have an active turn; idle is open without
// a turn and can own work. A turn that ends with no responsibilities makes the
// session dormant; its own turn runner makes that change.
// Coordination posts can wake dormant sessions; notices cannot. Maintenance
// closes them after DormantCloseAfter. Closed sessions require explicit Reopen.
// Metadata stores idle/dormant/closed; Info derives queued/running from turn state.
const (
	StatusIdle    = "idle"
	StatusDormant = "dormant"
	StatusQueued  = "queued"  // a turn is preparing its prompt
	StatusRunning = "running" // a turn is executing
	StatusClosed  = "closed"
)

// Turn statuses.
const (
	TurnQueued  = "queued"
	TurnRunning = "running"
	TurnOK      = "ok"
	TurnError   = "error"
	TurnStopped = "stopped"
	// TurnUnreadable is a turn whose record could not be read. Turns lists it
	// with the read error, so a view shows the problem instead of a gap.
	TurnUnreadable = "unreadable"
)

var (
	ErrNotFound = errors.New("no such subagent session")
	ErrClosed   = errors.New("subagent session is closed")
	// ErrNoRoom marks a stored record without room identity. Such records come
	// from an older dyna; they are never routed to a default room.
	ErrNoRoom = errors.New("record was created by an older dyna and has no room; it cannot be used")
	// ErrBusy means a turn is already running and could not be steered.
	ErrBusy = errors.New("a turn is already running")
)

// Meta is sessions/<id>/session.json.
type Meta struct {
	HistoryOnly bool `json:"historyOnly,omitempty"`
	// Service marks managed sessions and their helpers for mention routing and member grouping.
	Service bool `json:"service,omitempty"`
	// Leaf marks a session that can start neither subagent sessions nor
	// workflow runs; it completes its task itself. Workflow workers are
	// started as leaf sessions.
	Leaf bool `json:"leaf,omitempty"`
	// Run names the workflow run that owns a leaf session and the agent call it
	// serves. Such a session reports to its run, not to a parent session, and
	// is no member of its room: it is outside member lists, selectors and
	// check-ins, and the run closes it when its agent() call resolves.
	Run               *RunOwner       `json:"run,omitempty"`
	Role              string          `json:"role,omitempty"`
	InheritedReceipts map[string]bool `json:"inheritedReceipts,omitempty"`
	// UncertainInputIDs names posts a repaired turn had handed to its harness
	// with no delivery receipt. The harness may already have run them, so no
	// later turn admits them again until the owner resolves the input.
	UncertainInputIDs    []string             `json:"uncertainInputIds,omitempty"`
	ConsolidationHistory []ConsolidationState `json:"consolidationHistory,omitempty"`
	ID                   string               `json:"id"`
	Name                 string               `json:"name,omitempty"`
	RoomID               string               `json:"roomId,omitempty"`
	Room                 string               `json:"room,omitempty"`
	// ClosedPosts excludes address reuse while this exact session was closed.
	ClosedPosts map[string]bool `json:"closedPosts,omitempty"`
	// RoomCursor is the byte offset in this session's room message log up
	// to which posts addressed to this session have been accounted for at a turn boundary.
	RoomCursor int64 `json:"roomCursor,omitempty"`
	// OwnerDMCursor is the room log offset up to which the internal root has
	// read the owner's DMs to agents. It moves only when a root turn's input
	// includes them; a live gate moves RoomCursor past DMs it does not take.
	OwnerDMCursor        *int64              `json:"ownerDmCursor,omitempty"`
	Profile              string              `json:"profile"`
	Model                string              `json:"model,omitempty"`
	Transfer             *TransferState      `json:"transfer,omitempty"`
	Consolidation        *ConsolidationState `json:"consolidation,omitempty"`
	Consolidating        string              `json:"consolidating,omitempty"`
	ConsumedBy           string              `json:"consumedBy,omitempty"`
	ConsolidationCharter string              `json:"consolidationCharter,omitempty"`
	InheritedInboxes     []InheritedInbox    `json:"inheritedInboxes,omitempty"`
	// FallbackPath lists the profiles that automatic usage-limit fallback has
	// already moved this session to, so a chain never loops.
	FallbackPath []string `json:"fallbackPath,omitempty"`
	Dir          string   `json:"dir"`

	// Depth is 1 for a session started outside any session, parent+1 otherwise.
	Depth         int    `json:"depth"`
	ParentSession string `json:"parentSession,omitempty"`
	// Goal is the ID of the goal created with this session, or the parent
	// goal a delegation item was added to.
	Goal string `json:"goal,omitempty"`
	// Item is the ID of this session's item on Goal, set when the session was
	// started as a delegation instead of a goal of its own.
	Item string `json:"item,omitempty"`
	// SpawnBrief is this session's own first-turn brief. It preserves a new
	// brief when the session joins a goal that already has different detail.
	SpawnBrief string `json:"spawnBrief,omitempty"`
	// Owner is the agent session that started this one (runstore.OwnerFromEnv:
	// the parent session id, or claude:<session> for a harness
	// session outside dyna). Events and hooks follow "my children" by it.
	Owner        string    `json:"owner,omitempty"`
	Created      time.Time `json:"created"`
	LastActivity time.Time `json:"lastActivity"`
	// Finished records when dormancy began, for the maintenance cutoff.
	// Status alone determines dormancy; the next turn clears this timestamp.
	Finished time.Time `json:"finished,omitzero"`
	// Status stores idle, dormant or closed. Use Info.Status for live turn state.
	Status string `json:"status"`
	// Closed records the close time.
	Closed time.Time `json:"closed,omitempty"`
	Turns  int       `json:"turns"`
	// WrapUpAfter is the last real turn checked for a wrap-up, including an empty check.
	WrapUpAfter int                  `json:"wrapUpAfter,omitempty"`
	Closing     *sessionlife.Closing `json:"closing,omitempty"`
	// HarnessSession is the exact harness session id follow-up turns resume.
	HarnessSession string       `json:"harnessSession,omitempty"`
	RuntimePin     *runtime.Pin `json:"runtimePin,omitempty"`
	// Instructions is the frozen snapshot this harness conversation received.
	Instructions *instructions.Snapshot `json:"instructions,omitempty"`
	// InstructionsStale asks for a new snapshot on the next turn, because the
	// conversation was compacted and its cached prefix is gone.
	InstructionsStale bool `json:"instructionsStale,omitempty"`
	// InstructionsKnown is what the session was last told: its frozen
	// snapshot plus the change notes since. InstructionsNotes are those
	// notes' post IDs; a refresh cancels the ones it covers.
	InstructionsKnown *instructions.Snapshot `json:"instructionsKnown,omitempty"`
	InstructionsNotes []string               `json:"instructionsNotes,omitempty"`
	// InstructionsSeq counts the instruction notes this session has posted. It
	// is the note's occurrence identity, and unlike InstructionsNotes it never
	// resets when a snapshot refresh covers the notes.
	InstructionsSeq int `json:"instructionsSeq,omitempty"`
	// InstructionsChangedAt is the last queued change, persisted for the engine's quiet period.
	InstructionsChangedAt time.Time `json:"instructionsChangedAt,omitzero"`
	// OwnerDisplay lets this session's own shells use the owner's display.
	// Only the owner or root grants it at start, and no session inherits it;
	// root keeps the owner display without it.
	OwnerDisplay bool `json:"ownerDisplay,omitempty"`
	// Resumable records whether the profile's harness can resume (follow-ups
	// and steering need it).
	Resumable bool               `json:"resumable"`
	Usage     workeroutput.Usage `json:"usage"`
	// UsageBaseline is a checkpoint, not additional activity or repaired history.
	UsageBaseline     *workeroutput.UsageBaseline `json:"usageBaseline,omitempty"`
	UsageBaselineTurn int                         `json:"usageBaselineTurn,omitempty"`
	// ContextTokens is the prompt size of the last model call in the harness
	// session, when the harness reports it. It decides Dyna's compact command.
	ContextTokens int64 `json:"contextTokens,omitempty"`
	// Pid is the runner of the most recent turn (informational).
	Pid int `json:"pid,omitempty"`
}

// EffectiveName is the persisted name. Retained sessions without one get
// their generated name from sessionlife.Migrate.
func (m Meta) EffectiveName() string { return m.Name }

// Address returns this session's readable room address.
func (m Meta) Address() string {
	return m.Room + ":" + m.EffectiveName()
}

// Steer is one steering message accepted for a turn.
type Steer struct {
	// ID is the room post this steer delivered.
	ID        string `json:"id,omitempty"`
	TS        int64  `json:"ts"`
	Message   string `json:"message"`
	Delivered bool   `json:"delivered,omitempty"`
	// SourceOffset is a complete-line boundary in output.jsonl.
	// Nil means unknown; zero is a known boundary before any output.
	SourceOffset *int64 `json:"sourceOffset,omitempty"`
}

// Delivery records a deferred steering/journal delivery (tool boundaries).
type Delivery struct {
	Reason   string `json:"reason,omitempty"`
	TS       int64  `json:"ts"`
	Kind     string `json:"kind"`
	Outcome  string `json:"outcome"`
	WaitedMs int64  `json:"waitedMs,omitempty"`
}

// Turn is sessions/<id>/turns/<n>.json.
type Turn struct {
	// InputCutoff bounds posts present when a turn ended, to prevent automatic
	// retries of failed turns. A fresh post beyond it can start another turn.
	InputCutoff *int64 `json:"inputCutoff,omitempty"`
	// StartCutoff is the input boundary when the turn started: posts before it
	// were read, and a post after it wakes the session again. A repaired turn
	// uses it as its input cutoff, so input queued while that turn ran is not
	// mistaken for input it already read.
	StartCutoff     *int64                  `json:"startCutoff,omitempty"`
	Deadline        time.Time               `json:"deadline,omitempty"`
	RoomHeld        bool                    `json:"roomHeld,omitempty"`
	PendingInputIDs []string                `json:"pendingInputIds,omitempty"`
	HeldAccepted    bool                    `json:"heldAccepted,omitempty"`
	HeldPrompt      string                  `json:"heldPrompt,omitempty"`
	WrapUp          bool                    `json:"wrapUp,omitempty"`
	Harness         string                  `json:"harness,omitempty"`
	OutputDialect   string                  `json:"outputDialect,omitempty"`
	Profile         string                  `json:"profile,omitempty"`
	Model           string                  `json:"model,omitempty"`
	AccountSwitch   *harness.AccountSwitch  `json:"accountSwitch,omitempty"` // the turn moved to another account after a usage limit
	Fallbacks       []harness.ModelFallback `json:"fallbacks,omitempty"`     // moves to the next model in the profile's chain
	TransferID      string                  `json:"transferId,omitempty"`
	N               int                     `json:"n"`
	// Wake is why this turn started. Empty on a turn stored before the field existed.
	Wake               WakeCause                       `json:"wake,omitzero"`
	Prompt             string                          `json:"prompt"`
	Schema             string                          `json:"schema,omitempty"`
	Status             string                          `json:"status"`
	Created            time.Time                       `json:"created"`
	Started            time.Time                       `json:"started,omitempty"`
	Ended              time.Time                       `json:"ended,omitempty"`
	Result             any                             `json:"result,omitempty"`
	Error              string                          `json:"error,omitempty"`
	WorkerStderr       string                          `json:"workerStderr,omitempty"`  // the failed worker's stderr tail; detail for the owner, never cause text
	FailureReason      providerfailure.Reason          `json:"failureReason,omitempty"` // the provider failure a trusted boundary named; Error is display text
	HarnessSession     string                          `json:"harnessSession,omitempty"`
	Steering           []Steer                         `json:"steering,omitempty"`
	Deliveries         []Delivery                      `json:"deliveries,omitempty"`
	Usage              workeroutput.Usage              `json:"usage"`
	UsageNormalization workeroutput.UsageNormalization `json:"usageNormalization,omitempty"`
	// Byte ranges of the session's append-only raw and normalized output
	// that belong to this turn.
	RawStart int64 `json:"rawStart"`
	RawEnd   int64 `json:"rawEnd,omitempty"`
	// NormStart/NormEnd bound the normalized output events of this turn.
	NormStart  int64  `json:"normStart"`
	NormEnd    int64  `json:"normEnd,omitempty"`
	StopAsked  bool   `json:"stopAsked,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	// StopBy names who asked for the stop: a session's address, or "owner".
	StopBy string `json:"stopBy,omitempty"`
	// ReportTo records who made this turn owe a report: the immediate parent
	// session and/or the room owner. It preserves obligation provenance;
	// successful reports go to the current parent, or the owner if unavailable; failures also
	// alert the current available parent. An ok turn
	// with an empty list ends silently. See Owes.
	ReportTo []string `json:"reportTo,omitempty"`
	// PostedTo records recipients of explicit posts sent during this turn.
	PostedTo []string `json:"postedTo,omitempty"`
	// ReportPending keeps completion recoverable until its post or silent
	// result is durable. ReportPlan freezes a prepared post across retries.
	ReportPending bool        `json:"reportPending,omitempty"`
	ReportPlan    *ReportPlan `json:"reportPlan,omitempty"`
	// Reported is final. Historical reported turns are never replayed.
	Reported bool `json:"reported,omitempty"`
	// Accepted records that the harness took this turn's prompt. With the
	// carried post IDs it proves delivery even when no steering record names
	// the post. A repaired turn never accepted, so its posts stay retryable.
	Accepted bool `json:"accepted,omitempty"`
	// CarriedIDs names the room posts this turn took as input. Completion and
	// repair clear PendingInputIDs, so delivery must not depend on that pending
	// list: the carried record lasts as long as the turn row.
	CarriedIDs []string `json:"carriedIds,omitempty"`
}

// DeliveredPost reports whether the turn read room post id: the harness took
// the turn's prompt and the post was carried input, or a steering record
// proves the post reached it. Claiming a post alone is not proof: a repaired
// turn never accepted and its post must stay retryable.
// DispatchedWithoutReceipt reports a steer this turn handed to its harness with
// no delivery receipt yet. The engine and the runner can die between the two, so
// automatic recovery must not run that input again: only the owner can decide.
func (t Turn) DispatchedWithoutReceipt(id string) bool {
	if id == "" {
		return false
	}
	for _, steer := range t.Steering {
		if steer.ID == id {
			return !steer.Delivered
		}
	}
	return false
}

func (t Turn) DeliveredPost(id string) bool {
	if id == "" {
		return false
	}
	for _, steer := range t.Steering {
		if steer.ID == id && steer.Delivered {
			return true
		}
	}
	if !t.Accepted {
		return false
	}
	for _, carried := range t.CarriedIDs {
		if carried == id {
			return true
		}
	}
	return false
}

// appendUnique adds id to list once.
func appendUnique(list []string, id string) []string {
	for _, have := range list {
		if have == id {
			return list
		}
	}
	return append(list, id)
}

// Owe records addr as a source of the turn's report obligation once.
func (t *Turn) Owe(addr string) {
	if addr == "" {
		return
	}
	for _, a := range t.ReportTo {
		if a == addr {
			return
		}
	}
	t.ReportTo = append(t.ReportTo, addr)
}

// Done reports whether the turn reached a terminal status.
func (t Turn) Done() bool {
	return sessionlife.TerminalTurn(t.Status)
}

// Info is a session as listed: stored meta plus live status.
type Info struct {
	Meta
	// CloseSummary is what a finished close did with the session's work, one
	// typed fact per step. It is empty for an open session.
	CloseSummary []sessionclose.Outcome `json:"closeSummary,omitempty"`
	Status       string                 `json:"status"`
	CurrentTurn  int                    `json:"currentTurn,omitempty"`
}

// Store addresses the sessions directory. The zero value uses the data dir.
type Store struct {
	Root string
	// Now overrides the clock for dormant session tests.
	Now        func() time.Time
	decodeMeta func([]byte, any) error
	decodeTurn func([]byte, any) error
	// ReportRepaired, when set, reports a turn that RepairTurn just marked
	// failed. Application constructors supply the callback.
	ReportRepaired func(store *Store, id string, turn int)
	// versions, on a listing's copy of the store, holds the record versions
	// read once for the whole listing (see listing).
	versions map[string]sessionlife.Version
}

// DefaultStore uses <data>/sessions.
func DefaultStore() *Store {
	return &Store{Root: runstore.SessionsDir()}
}

func (s *Store) root() string {
	if s != nil && s.Root != "" {
		return s.Root
	}
	return runstore.SessionsDir()
}

var sessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateName checks a --name label.
func ValidateName(name string) error {
	if !sessionNamePattern.MatchString(name) {
		return fmt.Errorf("session name %q must be 1-64 characters of letters, digits, '.', '_' or '-' and start with a letter or digit", name)
	}
	if strings.HasPrefix(name, runstore.SessionDirPrefix) {
		return fmt.Errorf("session name %q must not start with %q (reserved for ids)", name, runstore.SessionDirPrefix)
	}
	if room.ReservedName(name) {
		return fmt.Errorf("session name %q is reserved for room mentions (@root, @team, @parent, @siblings, @children, and the removed @room and @owner)", name)
	}
	return nil
}

// ValidID reports an ID in the shape NewID mints:
// ses_<date>-<time>-<8 hex digits>, or 16 for a run agent's derived ID. The
// generated-name suffix and the worktree branch both rely on that shape.
func ValidID(id string) bool {
	rest, ok := strings.CutPrefix(id, runstore.SessionDirPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, "-")
	if len(parts) != 3 || len(parts[2]) != 8 && len(parts[2]) != 16 {
		return false
	}
	if _, err := time.Parse("20060102", parts[0]); err != nil {
		return false
	}
	if _, err := time.Parse("150405", parts[1]); err != nil {
		return false
	}
	_, err := hex.DecodeString(parts[2])
	return err == nil
}

// NewID mints a session id.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s%s-%s", runstore.SessionDirPrefix, time.Now().Format("20060102-150405"), hex.EncodeToString(b))
}

// Dir returns the session directory, validating the id shape.

// listing is a copy of the store that reads every record version in one
// query, with the session IDs it found, so a listing checks its cached
// records without a query per session. With open, closed records are left
// out by the query. Use it only to list: its versions are from the start of
// the listing.
func (s *Store) listing(open bool) (*Store, []string, error) {
	read := sessionlife.Versions
	if open {
		read = sessionlife.OpenVersions
	}
	versions, err := read(s.root())
	if errors.Is(err, sessionlife.ErrRecord) {
		err = nil
	}
	ids := make([]string, 0, len(versions))
	for id := range versions {
		if runstore.IsSessionDirID(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	listed := *s
	listed.versions = versions
	return &listed, ids, err
}

// recordVersion is session id's record version, from the listing when there is one.
func (s *Store) recordVersion(id string) (sessionlife.Version, bool) {
	if v, ok := s.versions[id]; ok {
		return v, v != ""
	}
	return sessionlife.RecordVersion(s.root(), id)
}

// sessionIDs lists the folders under the store that are named as sessions.
func (s *Store) sessionIDs() ([]string, error) {
	all, err := sessionlife.SafeIDs(s.root())
	var ids []string
	for _, id := range all {
		if runstore.IsSessionDirID(id) {
			ids = append(ids, id)
		}
	}
	return ids, err
}

func (s *Store) Dir(id string) (string, error) {
	if !runstore.IsSessionDirID(id) {
		return "", fmt.Errorf("invalid subagent session id %q", id)
	}
	return filepath.Join(s.root(), id), nil
}

func (s *Store) path(id string, elem ...string) (string, error) {
	dir, err := s.Dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, elem...)...), nil
}

// Paths of the session's output artifacts. The agents/1 layout matches runs
// so the output readers work unchanged.
func (s *Store) RawOutputPath(id string) (string, error) {
	return s.path(id, "agents", "1", "stdout.log")
}
func (s *Store) NormalizedPath(id string) (string, error) {
	return s.path(id, "agents", "1", "output.jsonl")
}
func (s *Store) EventsPath(id string) (string, error) { return s.path(id, "events.jsonl") }
func (s *Store) turnPath(id string, n int) (string, error) {
	return s.path(id, "turns", strconv.Itoa(n)+".json")
}

// withLock runs fn holding the session's short-lived metadata lock.
func (s *Store) withLock(id string, fn func() error) error {
	return s.withLockContext(context.Background(), id, fn)
}

func (s *Store) withLockContext(ctx context.Context, id string, fn func() error) error {
	unlock, err := s.lockSessionContext(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (s *Store) lockSessionContext(ctx context.Context, id string) (func(), error) {
	path, err := s.path(id, "session.lock")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w %q", ErrNotFound, id)
		}
		return nil, err
	}
	release := lockrank.Acquire(lockrank.Session)
	unlock, err := store.LockContext(ctx, path)
	if err != nil {
		release()
		return nil, err
	}
	return func() {
		unlock()
		release()
	}, nil
}

// readMetaRecord decodes one atomic snapshot without assuming room membership.
func (s *Store) readMetaRecord(id string) (Meta, error) {
	entry, err := s.metaSnapshot(id)
	if err != nil {
		return Meta{}, err
	}
	return cloneMeta(entry.meta), nil
}

// ReadMeta loads session.json and requires a complete room stamp.
func (s *Store) ReadMeta(id string) (Meta, error) {
	m, err := s.readMetaRecord(id)
	if err != nil {
		return Meta{}, err
	}
	if !m.HistoryOnly && (m.RoomID == "" || m.Room == "") {
		return Meta{}, fmt.Errorf("%w: session %s has no roomId", ErrNoRoom, id)
	}
	return m, nil
}

func (s *Store) writeMeta(m Meta) error { return s.writeMetaIf(context.Background(), m, nil) }

func normalizeMeta(m Meta) Meta {
	if !m.Closed.IsZero() {
		m.Status = StatusClosed
	} else if m.Status == "" {
		m.Status = StatusIdle
	}
	return m
}

// writeMetaIf is writeMeta that writes only while still accepts the stored
// record, read in the same transaction as the write.
func (s *Store) writeMetaIf(ctx context.Context, m Meta, still func(Meta) error) error {
	m = normalizeMeta(m)
	if _, err := s.Dir(m.ID); err != nil {
		return err
	}
	save := func() error { return sessionlife.SaveContext(ctx, s.root(), m.ID, m) }
	if still != nil {
		save = func() error {
			return sessionlife.SaveIf(s.root(), m.ID, m, func(raw []byte) error {
				var cur Meta
				if err := json.Unmarshal(raw, &cur); err != nil {
					return err
				}
				return still(cur)
			})
		}
	}
	if err := save(); err != nil {
		return err
	}
	return s.metaWritten(m)
}

func (s *Store) metaWritten(m Meta) error {
	infoCache.Delete(s.recordKey(m.ID)) // promotion can also change a closed descendant
	// All close paths (including finish and consolidation) persist here.
	// Do not acquire the publication lock: finish can already hold it.
	if !m.Closed.IsZero() {
		return (&shells.Store{Root: filepath.Join(filepath.Dir(s.root()), "shells")}).StopOwner(m.ID)
	}
	return nil
}

// UpdateMeta applies fn to session.json under the session lock.
func (s *Store) UpdateMeta(id string, fn func(*Meta)) (Meta, error) {
	return s.updateMetaContext(context.Background(), id, fn)
}

func (s *Store) updateMetaContext(ctx context.Context, id string, fn func(*Meta)) (Meta, error) {
	var out Meta
	err := s.withLockContext(ctx, id, func() error {
		m, err := s.ReadMeta(id)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fn(&m)
		out = m
		return s.writeMetaIf(ctx, m, nil)
	})
	return out, err
}

// updateMetasContext applies fn to each session in ids under that session's
// lock, then saves every session fn changed in one dyna.db write. It returns
// the saved sessions. The write publishes one session event with an empty
// key, which tells clients that every session may have changed.
func (s *Store) updateMetasContext(ctx context.Context, ids []string, fn func(*Meta) bool) ([]Meta, error) {
	var locked []string
	for _, id := range ids {
		dir, err := s.Dir(id)
		if err != nil {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			locked = append(locked, id)
		}
	}
	var saved []Meta
	err := sessionlife.WithSessionLocks(s.root(), locked, func() error {
		var metas []Meta
		for _, id := range locked {
			m, err := s.ReadMeta(id)
			if err != nil {
				continue
			}
			if fn(&m) {
				metas = append(metas, normalizeMeta(m))
			}
		}
		if err := ctx.Err(); err != nil || len(metas) == 0 {
			return err
		}
		db, err := database.Open(filepath.Dir(s.root()))
		if err != nil {
			return err
		}
		err = state.For(db).Write(ctx, hub.Event{Topic: sessionlife.Topic}, func(conn *sql.Conn) error {
			for _, m := range metas {
				if err := sessionlife.SaveTx(conn, m.ID, m); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, m := range metas {
			if err := s.metaWritten(m); err != nil {
				return err
			}
		}
		saved = metas
		return nil
	})
	return saved, err
}

// ReadTurn loads one turn and its prompt text from the same dyna.db snapshot.
func (s *Store) ReadTurn(id string, n int) (Turn, error) {
	b, _, err := s.readTurnRow(id, n)
	if err != nil {
		return Turn{}, err
	}
	return s.decodeTurnRecord(id, n, b)
}

func (s *Store) decodeTurnRecord(id string, n int, b []byte) (Turn, error) {
	var t Turn
	decode := s.decodeTurn
	if decode == nil {
		decode = json.Unmarshal
	}
	if err := decode(b, &t); err != nil {
		return Turn{}, fmt.Errorf("parse turn %d of %s: %w", n, id, err)
	}
	return t, nil
}

// steering returns the steering of session id's turns 1 to turns, in turn
// order: finished turns from dyna.db, the others from their files.
func (s *Store) steering(id string, turns int) ([]sessionlife.Steer, error) {
	return sessionlife.Steering(s.root(), id, turns, func(n int) ([]sessionlife.Steer, bool, error) {
		t, err := s.ReadTurn(id, n)
		if err != nil {
			return nil, false, err
		}
		var out []sessionlife.Steer
		for _, st := range t.Steering {
			if st.ID != "" {
				out = append(out, sessionlife.Steer{Turn: n, ID: st.ID, Delivered: st.Delivered})
			}
		}
		return out, t.Done(), nil
	})
}

// deliveredIDs is the set of posts any of session id's turns 1 to turns
// delivered.
func (s *Store) deliveredIDs(id string, turns int) (map[string]bool, error) {
	steers, err := s.steering(id, turns)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, st := range steers {
		if st.Delivered {
			out[st.ID] = true
		}
	}
	return out, nil
}

// writeTurn stores turn t of session id in dyna.db. Callers hold the session lock.
func (s *Store) writeTurn(id string, t Turn) error {
	return s.writeTurnContext(context.Background(), id, t)
}

// writeTurnContext is writeTurn for a call: ctx's request ID is recorded in
// the same transaction as the row.
func (s *Store) writeTurnContext(ctx context.Context, id string, t Turn) error {
	_, err := s.turnPath(id, t.N)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	_, err = saveTurnRow(ctx, db, id, t.N, raw)
	return err
}

// UpdateTurn applies fn to a turn record under the session lock.
func (s *Store) UpdateTurn(id string, n int, fn func(*Turn)) (Turn, error) {
	return s.updateTurnContext(context.Background(), id, n, fn)
}

func (s *Store) updateTurnContext(ctx context.Context, id string, n int, fn func(*Turn)) (Turn, error) {
	var out Turn
	err := s.withLockContext(ctx, id, func() error {
		t, err := s.readTurnForUpdate(id, n)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fn(&t)
		out = t
		return s.writeTurnContext(ctx, id, t)
	})
	return out, err
}

// Turns lists every turn record in order from one database snapshot. A turn
// that cannot be read is listed as TurnUnreadable with its read error.
func (s *Store) Turns(id string) ([]Turn, error) {
	m, err := s.ReadMeta(id)
	if err != nil {
		return nil, err
	}
	return s.turnRange(id, 1, m.Turns)
}

// TurnRange lists turns first through last, inclusive, from one database snapshot.
func (s *Store) TurnRange(id string, first, last int) ([]Turn, error) {
	if first < 1 || last < first {
		return nil, fmt.Errorf("invalid turn range %d through %d", first, last)
	}
	return s.turnRange(id, first, last)
}

func (s *Store) turnRange(id string, first, last int) ([]Turn, error) {
	db, err := s.turnsDB()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	turns := make([]Turn, 0, max(0, last-first+1))
	err = state.For(db).Read(ctx, func(r state.Reader) error {
		for n := first; n <= last; n++ {
			var turn Turn
			raw, _, err := resolvedTurn(ctx, r, id, n)
			if errors.Is(err, sql.ErrNoRows) {
				err = fmt.Errorf("session %s has no turn %d", id, n)
			}
			if err == nil {
				turn, err = s.decodeTurnRecord(id, n, raw)
			}
			if err != nil {
				turn = Turn{N: n, Status: TurnUnreadable, Error: err.Error()}
			}
			turns = append(turns, turn)
		}
		return nil
	})
	return turns, err
}

// lockHeld reports whether a turn runner holds the lock, where its identity is known.
func (s *Store) lockHeld(id string) bool {
	dir, err := s.Dir(id)
	if err != nil {
		return false
	}
	return turnlock.Held(dir, id)
}

// TryLockTurnForTest holds a session's turn lock for tests of deferral.
func (s *Store) TryLockTurnForTest(id string) (*os.File, error) { return s.tryTurnLock(id) }

// tryTurnLock takes the turn lock without blocking. The caller owns the
// returned file; closing it releases the lock.
func (s *Store) tryTurnLock(id string) (*os.File, error) {
	path, err := s.path(id, "turn.lock")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

// Info resolves the live status of a session, repairing a turn whose runner
// died without finishing (lock released, record still queued/running).
func (s *Store) Info(id string) (Info, error) { return s.info(id, true) }

// Inspect returns live session status without repairing turns or reporting results.
func (s *Store) Inspect(id string) (Info, error) { return s.info(id, false) }

func (s *Store) info(id string, repair bool) (Info, error) {
	m, entry, err := s.cachedMeta(id)
	if err != nil {
		return Info{}, err
	}
	info := Info{Meta: m, Status: m.Status}
	// Close stops the turn first and closed sessions start none, so listings
	// skip the turn and lock probes for the many closed sessions.
	if m.Turns == 0 || info.Status == StatusClosed {
		return info, nil
	}
	turn, err := s.cachedTurn(id, m.Turns, entry)
	if err != nil {
		return info, nil
	}
	if s.lockHeld(id) {
		info.CurrentTurn = turn.n
		info.Status = StatusRunning
		if turn.status == TurnQueued {
			info.Status = StatusQueued
		}
		return info, nil
	}
	if !turn.done && turn.pending {
		info.Status, info.CurrentTurn = StatusQueued, turn.n
		return info, nil
	}
	if repair && !turn.done {
		// Runner is gone: repair the record so the session is usable again,
		// and report the failure.
		if repaired, _ := s.RepairTurn(id, turn.n, "turn runner exited without recording a result"); repaired && s.ReportRepaired != nil {
			s.ReportRepaired(s, id, turn.n)
		}
	}
	return info, nil
}

// repairProcess is this program and its subcommand, such as "dyna engine".
func repairProcess() string {
	p := filepath.Base(os.Args[0])
	if len(os.Args) > 1 {
		p += " " + os.Args[1]
	}
	return p
}

// repairCaller names the first functions above RepairTurn and info, the
// read that asked for the repair.
func repairCaller() string {
	pcs := make([]uintptr, 16)
	frames := goruntime.CallersFrames(pcs[:goruntime.Callers(3, pcs)])
	var names []string
	for len(names) < 4 {
		frame, more := frames.Next()
		names = append(names, strings.TrimPrefix(frame.Function, "dyna-agent/internal/"))
		if !more {
			break
		}
	}
	return strings.Join(names, " < ")
}

// RepairTurn marks an orphaned turn as failed and reports whether it did. It
// is a no-op while a runner still holds the lock, while the gate's runner
// process is alive, or when the turn already finished.
func (s *Store) RepairTurn(id string, n int, reason string) (bool, error) {
	repaired := false
	err := s.withLock(id, func() error {
		if s.lockHeld(id) {
			return nil
		}
		// A runner releases turn.lock before it exits, and a turn it finished
		// was seen repaired a few milliseconds later. Never repair while the
		// gate's runner process is alive, whatever the row says; once it
		// exits, a later read repairs a turn that is still not finished. An
		// in-process runner (InProcessSpawner, tests) is this process, so
		// only its lock tells whether it runs.
		if dir, err := s.Dir(id); err == nil && readGateState(dir).Pid != os.Getpid() {
			if alive, _ := turnlock.RunnerAlive(dir, id); alive {
				return nil
			}
		}
		t, err := s.readTurnForUpdate(id, n)
		if err != nil || t.Done() || t.RoomHeld || t.HeldPrompt != "" && !t.HeldAccepted {
			return err
		}
		// Record what this repair read and who made it, so a turn whose runner
		// finished but that a reader still repaired names its cause. Measure the
		// input boundary first: a terminal turn without one would let a later
		// wake retry input this turn already read.
		meta, err := s.ReadMeta(id)
		if err != nil {
			return err
		}
		// The turn measured its input boundary when it started, so input queued
		// while it ran is still fresh: a dead turn never read that input. Only a
		// record from before that measurement falls back to the size now.
		cutoff := t.StartCutoff
		if cutoff == nil {
			rooms := &room.Store{Root: filepath.Join(filepath.Dir(s.root()), "rooms")}
			cutoff, err = terminalCutoff(rooms, meta.RoomID)
			if err != nil {
				return err
			}
		}
		t.InputCutoff = cutoff
		// Save the quarantine first. A repair that stops after the terminal turn
		// would never record it again, because a later repair sees a finished
		// turn, and the uncertain input could then enter another turn.
		if _, err := s.quarantineUncertainInput(&meta, t); err != nil {
			return err
		}
		readRevision, _ := s.turnVersion(id, n)
		var gate gateState
		if dir, err := s.Dir(id); err == nil {
			gate = readGateState(dir)
		}
		s.appendJournal(id, TurnRepairedJournal{Turn: n, ReadStatus: t.Status, ReadRevision: readRevision,
			GatePid: gate.Pid, GateTurn: gate.Turn, GateActive: gate.Active, Pid: os.Getpid(), Process: repairProcess(), Caller: repairCaller()})
		t.Status = TurnError
		t.RoomHeld, t.HeldPrompt, t.HeldAccepted = false, "", false
		t.PendingInputIDs = nil
		t.Error = reason
		t.Ended = time.Now()
		if err := s.writeTurn(id, t); err != nil {
			return err
		}
		repaired = true
		return nil
	})
	return repaired, err
}

// quarantineUncertainInput saves the live input a dead runner handed to its
// harness without a receipt, so no later turn admits it again. The write happens
// before the terminal turn, in its own transaction: a repair that stops after the
// turn would leave the quarantine unsaved forever.
func (s *Store) quarantineUncertainInput(meta *Meta, t Turn) (bool, error) {
	added := false
	for _, steer := range t.Steering {
		if steer.ID == "" || steer.Delivered || slices.Contains(meta.UncertainInputIDs, steer.ID) {
			continue
		}
		// Input the turn drained at its start was read from the room log and
		// stays with the claim that guards it; a live post the runner handed over
		// mid-turn is the uncertain one.
		if slices.Contains(t.PendingInputIDs, steer.ID) {
			continue
		}
		meta.UncertainInputIDs = append(meta.UncertainInputIDs, steer.ID)
		added = true
	}
	if !added {
		return false, nil
	}
	if err := s.writeMeta(*meta); err != nil {
		return false, err
	}
	return true, nil
}

// ReportTarget supplies the obligation source for a first or schema turn:
// its open parent in the same room, else the room owner. This does not set
// delivery recipients; failure delivery uses the current stored parent.
func (s *Store) ReportTarget(m Meta) string {
	if m.ParentSession != "" {
		if parent, err := s.Inspect(m.ParentSession); err == nil && parent.Status != StatusClosed && parent.RoomID == m.RoomID {
			return parent.Address()
		}
	}
	return m.Room
}

// Owes reports whether post msg makes a turn of session m owe a report. Only
// a post from the immediate parent session or the room owner does, and only
// when it names m itself. Input that came only through a selector, such as
// @team or @children, owes nothing. Failed and stopped turns always report.
// The internal root answers the human: the owner's request
// (room.OwnerRequest), or an owner post to the root session itself such as a
// handoff, owes a report.
func (s *Store) Owes(m Meta, msg room.Message) bool {
	if m.Role == RoleRoot {
		// The room's own address means the owner, except for a typed system
		// event: the engine posts those, and an engine note owes no report.
		return room.OwnerRequest(msg, m.Room) || msg.From == m.Room && !msg.EngineEvent() && msg.SessionAddressed(m.ID, m.Address())
	}
	from := msg.From
	if from == "" || !s.named(m, msg) {
		return false
	}
	if from == m.Room {
		return !msg.EngineEvent()
	}
	if m.ParentSession == "" {
		return false
	}
	parent, err := s.ReadMeta(m.ParentSession)
	return err == nil && msg.SentBySession(parent.ID, parent.Address())
}

// named reports whether msg names session m itself: by ID, address or bare
// name, or by a consumed name whose delivery alias leads to m. A post with no
// recorded mentions names every recipient.
func (s *Store) named(m Meta, msg room.Message) bool {
	refs, recorded := msg.Named()
	if !recorded {
		return true
	}
	// Only a consolidation target can own a delivery alias. Every other
	// session is named by its own ID, address or name, so a broadcast post
	// costs one string check per mention and never scans the store.
	aliases := m.Consolidation != nil || m.ConsolidationCharter != "" ||
		len(m.InheritedInboxes) > 0 || len(m.ConsolidationHistory) > 0
	for _, ref := range refs {
		// A bare name is relative to the room; an ID or address is not.
		for _, c := range []string{ref, m.Room + ":" + ref} {
			if c == m.ID || c == m.Address() || msg.IsSession(c, m.ID, m.Address()) {
				return true
			}
			if !aliases {
				continue
			}
			if target, err := s.InspectDeliveryTarget(c); err == nil && target.ID == m.ID {
				return true
			}
		}
	}
	return false
}

// List returns sessions newest first. Closed sessions are included only when all.
func (s *Store) List(all bool) ([]Info, error) { return s.list(all, true) }

// Revision changes whenever any session record is written or removed.
func (s *Store) Revision() (string, error) { return sessionlife.Revision(s.root()) }

// ListSnapshot lists live session status without repair or completion side effects.
func (s *Store) ListSnapshot(all bool) ([]Info, error) { return s.list(all, false) }

func (s *Store) list(all, repair bool) ([]Info, error) {
	s, ids, err := s.listing(!all)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, id := range ids {
		info, err := s.info(id, repair)
		if err != nil {
			continue
		}
		if !all && info.Status == StatusClosed {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// RoomMemberCount counts visible members and live turns for a room.
type RoomMemberCount struct {
	Members int
	Running int
}

// RoomMemberCounts counts open sessions only, so closed members cost nothing
// on each desktop refresh. A room with no open session has no entry. It
// inspects each session without repairing it: a read never writes, so a
// stalled write cannot hold the room list (the room watch repairs).
func (s *Store) RoomMemberCounts() (map[string]RoomMemberCount, error) {
	s, ids, err := s.listing(true)
	if err != nil {
		return nil, err
	}
	out := map[string]RoomMemberCount{}
	for _, id := range ids {
		meta, _, err := s.cachedMeta(id)
		if err != nil {
			continue
		}
		count := out[meta.RoomID]
		if meta.Closed.IsZero() {
			info, err := s.Inspect(meta.ID)
			if err != nil {
				continue
			}
			if info.Status != StatusClosed {
				count.Members++
			}
			if info.Status == StatusRunning || info.Status == StatusQueued {
				count.Running++
			}
		}
		out[meta.RoomID] = count
	}
	return out, nil
}

// Resolve finds a session by ID, full room address, or an unambiguous bare
// name.
func (s *Store) Resolve(ref string) (Info, error) { return s.resolve(ref, true) }

// ResolveSnapshot resolves the same selectors as Resolve without repair or reporting.
func (s *Store) ResolveSnapshot(ref string) (Info, error) { return s.resolve(ref, false) }

func (s *Store) resolve(ref string, repair bool) (Info, error) {
	meta, err := s.resolveMeta(ref)
	if err != nil {
		return Info{}, err
	}
	return s.info(meta.ID, repair)
}

func (s *Store) resolveMeta(ref string) (Meta, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Meta{}, fmt.Errorf("session id or address is required")
	}
	if runstore.IsSessionDirID(ref) {
		return s.ReadMeta(ref)
	}
	// Stored session addresses include the internal root, whose name is
	// reserved in message selectors but valid for session lookup.
	room, name, address := "", ref, false
	if r, n, ok := strings.Cut(ref, ":"); ok {
		room, name, address = r, n, true
	}
	matches, err := s.nameMatches(room, name)
	if err != nil {
		return Meta{}, err
	}
	if address {
		if room == "" || len(matches) == 0 {
			return Meta{}, fmt.Errorf("%w %q (no open session has that address)", ErrNotFound, ref)
		}
		return matches[0], nil
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Meta{}, fmt.Errorf("%w %q (no open session has that id, address, or name)", ErrNotFound, ref)
	default:
		addrs := make([]string, len(matches))
		for i, info := range matches {
			addrs[i] = info.Address()
		}
		return Meta{}, fmt.Errorf("session name %q is ambiguous; use a full address: %s", ref, strings.Join(addrs, ", "))
	}
}

// nameMatches returns the sessions that answer to name, newest first, from one
// indexed lookup: room narrows an address lookup, and an empty room finds the
// name in every room. A name lookup runs while the recipient gate can be
// held, so it must never list the store.
//
// The answers keep the tolerance of a metadata scan: a record that cannot be
// read, a record whose stored name changed since the lookup, and a record
// without a room stamp are left out, and a closed session answers only while
// a consolidation consumed it, so a consumed name still redirects. Only the
// rows of dyna.db answer: a record that is still only a legacy session.json,
// which the engine imports at start, answers no name lookup.
func (s *Store) nameMatches(room, name string) ([]Meta, error) {
	ids, err := sessionlife.Lookup(s.root(), room, name)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, id := range ids {
		meta, assigned, err := s.readRoomMeta(id)
		if err != nil || !assigned {
			continue
		}
		if meta.EffectiveName() != name || (room != "" && meta.Room != room) {
			continue
		}
		if !meta.Closed.IsZero() && meta.ConsumedBy == "" {
			continue
		}
		out = append(out, meta)
	}
	return out, nil
}

// nameTaken reports whether an open session in one room already uses name.
func (s *Store) nameTaken(roomID, name string) (bool, error) {
	sessions, err := s.List(true)
	if err != nil {
		return false, err
	}
	for _, info := range sessions {
		if (info.Status != StatusClosed || info.ConsumedBy != "") && info.RoomID == roomID && info.EffectiveName() == name {
			return true, nil
		}
	}
	return false, nil
}

// appendJournal adds one line to the session's events.jsonl (debugging
// record). It stamps the line, so no writer has to.
func (s *Store) appendJournal(id string, payload JournalPayload) {
	path, err := s.EventsPath(id)
	if err != nil {
		return
	}
	b, err := json.Marshal(JournalEvent{TS: time.Now().UnixMilli(), Payload: payload})
	if err != nil {
		return
	}
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return
	}
	// A debugging record that nothing recovers from, so no fsync, and no
	// event while the room watch owns the members topic.
	_ = state.For(db).Append(context.Background(), hub.Event{}, path, b)
}

// Events reads the session event log. A line this version cannot read is left
// out, as it always was: the journal is a debugging record. A line of a kind
// this version does not know comes back whole, as an UnknownJournal.
func (s *Store) Events(id string) ([]JournalEvent, error) {
	path, err := s.EventsPath(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []JournalEvent
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev JournalEvent
		if json.Unmarshal([]byte(line), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out, nil
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
