package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dyna-agent/internal/lockrank"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/turnlock"
)

// Live delivery to a running turn.
//
// Every post to a session is stored once, in its room log. A session reads
// its posts from that log with a cursor: a turn's prompt drains the posts
// after meta.RoomCursor, and the turn gate then reads newer posts as live
// steering. There is no second copy of a post.
//
// The gate decides between "the running turn will read this post" and "the
// sender must start a follow-up turn". A sender appends its post first and
// then checks the gate under the gate lock. The runner's final read runs
// under the same lock: when it finds no new post it closes the gate and
// records the cursor as ClosedAt. So a post either lies before ClosedAt and
// was read by the turn, or the sender sees the gate closed and starts the
// next turn, whose prompt drains from ClosedAt. The next turn starts there.

const gateFile = turnlock.GateFile

type gateState struct {
	Active bool `json:"active"`
	// Pid is the runner that claimed the gate; 0 while the turn is queued.
	Pid  int `json:"pid"`
	Turn int `json:"turn"`
	// ClosedAt is the room log offset the turn read up to when the gate
	// closed. The next turn drains from here.
	ClosedAt int64 `json:"closedAt,omitempty"`
}

func (s *Store) withGateLock(id string, fn func(dir string) error) error {
	return s.withGateLockContext(context.Background(), id, fn)
}

func (s *Store) withGateLockContext(ctx context.Context, id string, fn func(dir string) error) error {
	dir, err := s.Dir(id)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w %q", ErrNotFound, id)
	}
	defer lockrank.Acquire(lockrank.Gate)()
	unlock, err := store.LockContext(ctx, filepath.Join(dir, "gate.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn(dir)
}

func readGateState(dir string) gateState {
	var st gateState
	if b, err := os.ReadFile(filepath.Join(dir, gateFile)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func writeGateState(dir string, st gateState) error {
	b, _ := json.Marshal(st)
	return os.WriteFile(filepath.Join(dir, gateFile), append(b, '\n'), 0o600)
}

// turnGate is one turn's live-delivery gate over the room log. It satisfies
// harness.SteeringSource: the turn runner activates it when the turn starts
// (a post may arrive before the harness has announced its session id; it is
// delivered as soon as the session can be resumed, or as the next
// continuation when the turn ends).
type turnGate struct {
	store *Store
	rooms *room.Store
	meta  Meta
	turn  int
	// cursor is the room log offset this turn has read up to.
	cursor       int64
	mu           sync.Mutex
	ctx          context.Context
	writes       *runnerWrites
	pending      *pendingInput
	waiting      *pendingInputWait
	liveDeadline func() (<-chan time.Time, func())
}

// The read phase never queues a mutation. Read alone consumes readiness
// under g.mu, so final sealing can cancel and join it without a late claim.
type pendingInputWait struct {
	cancel context.CancelFunc
	done   <-chan error
	wake   <-chan struct{}
}

type pendingInput struct {
	final bool
	done  <-chan inputReply
	wake  <-chan struct{}
}

type inputReply struct {
	input ClaimedInput
	err   error
}

// Only a live read may fail open. A final read must resolve the pending
// claim, because an empty final read tells the harness that input is sealed.
const liveInputWait = 250 * time.Millisecond

func liveInputDeadline() (<-chan time.Time, func()) {
	timer := time.NewTimer(liveInputWait)
	return timer.C, func() { timer.Stop() }
}

// newTurnGate returns the gate for turn n of the session, reading posts
// after cursor.
func (s *Store) newTurnGate(rooms *room.Store, meta Meta, turn int, cursor int64) *turnGate {
	m := &Manager{Store: s, Rooms: rooms}
	return &turnGate{store: s, rooms: rooms, meta: meta, turn: turn, cursor: cursor,
		ctx: context.Background(), writes: m.runnerWrites(meta.ID, turn), liveDeadline: liveInputDeadline}
}

// prepareGate opens the gate for a queued turn, before its runner starts. Pid 0 means the runner has not claimed it yet. Posts that
// arrive meanwhile stay in the room log and drain into the turn's prompt.
func (s *Store) prepareGate(id string, turn int) error {
	return s.withGateLock(id, func(dir string) error {
		return writeGateState(dir, gateState{Active: true, Turn: turn})
	})
}

// Activate claims the gate for this runner.
func (g *turnGate) Activate() error {
	return g.store.withGateLock(g.meta.ID, func(dir string) error {
		st := readGateState(dir)
		if st.Active && st.Turn == g.turn && st.Pid == os.Getpid() {
			return nil
		}
		return writeGateState(dir, gateState{Active: true, Pid: os.Getpid(), Turn: g.turn})
	})
}

// Read returns the posts to this session after the gate's cursor, framed
// for live steering. The offset argument is ignored: the gate keeps its own
// cursor, so a second harness run in one turn (a schema retry) never reads
// a post twice. With final set and nothing new, the gate closes atomically
// with respect to senders and records ClosedAt.
func (g *turnGate) Read(_ int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if final {
		g.writes.terminalContext()
		g.cancelInputWait()
	}
	var deadline <-chan time.Time
	if !final && g.writes.out != nil {
		var stop func()
		deadline, stop = g.liveDeadline()
		defer stop()
	}
	for {
		if !final && g.writes.out != nil && g.pending == nil {
			ready, err := g.waitForInput(deadline)
			if err != nil || !ready {
				return nil, g.cursor, err
			}
		}
		input := InputClaim{Turn: g.turn, Cursor: g.cursor, Final: final,
			Depth: g.meta.Depth, ParentSession: g.meta.ParentSession}
		var result inputReply
		wasFinal := final
		if g.writes.out == nil {
			result.input, result.err = g.writes.claimInput(g.ctx, input)
		} else {
			if g.pending == nil {
				done := make(chan inputReply, 1)
				wake := make(chan struct{})
				g.pending = &pendingInput{final: final, done: done, wake: wake}
				go func() {
					reply, err := g.writes.claimInput(g.ctx, input)
					done <- inputReply{input: reply, err: err}
					close(wake)
				}()
			}
			select {
			case result = <-g.pending.done:
				wasFinal = g.pending.final
				g.pending = nil
			case <-g.ctx.Done():
				return nil, g.cursor, g.ctx.Err()
			case <-deadline:
				// Keep the same outstanding call and cursor. Only a later
				// read that consumes its committed reply can advance input.
				return nil, g.cursor, nil
			}
		}
		if result.err != nil {
			return nil, g.cursor, result.err
		}
		g.cursor = result.input.Cursor
		g.meta.Depth, g.meta.ParentSession = result.input.Depth, result.input.ParentSession
		out := make([]runstore.SteeringMessage, 0, len(result.input.Posts))
		for _, post := range result.input.Posts {
			out = append(out, runstore.SteeringMessage{TS: post.Steer.TS, ID: post.Steer.ID,
				Message: post.Steer.Message, From: post.From})
		}
		if final && len(out) == 0 && !result.input.Closed {
			if !wasFinal {
				// The earlier live read was empty. Resolve an actual final
				// read before telling the harness that input is sealed.
				continue
			}
			return nil, g.cursor, fmt.Errorf("final input claim did not seal turn %d", g.turn)
		}
		return out, g.cursor, nil
	}
}

// Deactivate closes the gate at the cursor the turn has read up to, and
// records that cursor for closedAt. Posts after it drain into the next turn.
// A claim reply that has not been consumed yet keeps its cursor with the next
// turn, which recovers it: the old gate must not advance past input the
// harness never received.
func (g *turnGate) Deactivate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancelInputWait()
	_ = g.store.withGateLock(g.meta.ID, func(dir string) error {
		st := readGateState(dir)
		if st.Pid != os.Getpid() || st.Turn != g.turn || !st.Active {
			return nil
		}
		st.Active, st.ClosedAt = false, g.cursor
		return writeGateState(dir, st)
	})
}

// Settle joins an outstanding input claim through ctx and returns the room-log
// position up to which this turn read: the gate cursor, or the claim's
// committed cursor when that is later. It does not move the gate's published
// cursor, so a post the harness never received is still recovered by the next
// turn; the position only bounds what the failed turn may retry.
func (g *turnGate) Settle(ctx context.Context) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancelInputWait()
	boundary := g.cursor
	if g.pending != nil {
		take := func(reply inputReply) {
			if reply.err == nil && reply.input.Cursor > boundary {
				boundary = reply.input.Cursor
			}
		}
		select {
		case reply := <-g.pending.done:
			take(reply)
		case <-ctx.Done():
			select {
			case reply := <-g.pending.done:
				take(reply)
			default:
			}
		}
		g.pending = nil
	}
	return boundary
}

// closedAt returns where turn n's gate closed, if it did.
func (s *Store) closedAt(id string, turn int) (int64, bool) {
	var at int64
	ok := false
	_ = s.withGateLock(id, func(dir string) error {
		st := readGateState(dir)
		if st.Turn == turn && !st.Active && st.ClosedAt > 0 {
			at, ok = st.ClosedAt, true
		}
		return nil
	})
	return at, ok
}

// gateOpen reports whether the session's current turn will still read new
// posts: its gate is open and its runner holds the turn lock.
func (s *Store) gateOpen(id string) (int, bool, error) {
	turn, open := 0, false
	err := s.withGateLock(id, func(dir string) error {
		st := readGateState(dir)
		if st.Active && s.lockHeld(id) {
			turn, open = st.Turn, true
		}
		return nil
	})
	return turn, open, err
}

// forSession keeps the posts that are this session's input. The internal root
// takes the posts its stored seat delivery rows mark as its own input while it
// holds the root seat, and nothing while an external orchestrator holds it.
// Every other session keeps the posts its stored delivery rows name it in that
// it did not write.
//
// A read that fails returns an error and no posts: a caller must then keep its
// room cursor and its cached pending posts where they are, so the same posts
// are read again instead of being skipped for good.
func forSession(rooms *room.Store, meta Meta, msgs []room.Message) ([]room.Message, error) {
	received, err := sessionInput(rooms, meta, msgs)
	if err != nil {
		return nil, err
	}
	keep := func(msg room.Message) bool {
		return msg.Deliverable() && received(msg) && !msg.SentBySession(meta.ID, meta.Address())
	}
	if meta.Role == RoleRoot {
		if keep, err = seatInput(rooms, meta, msgs); err != nil {
			return nil, err
		}
	}
	var out []room.Message
	for _, msg := range msgs {
		if meta.ClosedPosts[msg.ID] {
			continue
		}
		if meta.Transfer != nil && meta.Transfer.Phase != "waiting" && msg.ID == meta.Transfer.PostID {
			continue
		}
		if keep(msg) {
			out = append(out, msg)
		}
	}
	return out, nil
}

// seatInput is the root seat's turn input, read from the seat's stored delivery
// rows: a post that asks the seat to act, or a passive one that only adds
// context to its next turn. Quiet posts with their own section in the root's
// context are not turn input, and the seat's own posts never come back to it.
// A failed row read is an error, so the seat's cursor stays where it is.
func seatInput(rooms *room.Store, meta Meta, msgs []room.Message) (func(room.Message) bool, error) {
	if rooms == nil {
		return func(room.Message) bool { return false }, nil
	}
	rec, err := rooms.Resolve(meta.RoomID)
	if err != nil || rec.Seat.External {
		return func(room.Message) bool { return false }, nil
	}
	seats, err := rooms.SeatDeliveries(meta.RoomID, postIDs(msgs))
	if err != nil {
		return nil, err
	}
	return func(msg room.Message) bool {
		seat, ok := seats[msg.ID]
		return ok && seat.RootInput() && !msg.SentBySession(meta.ID, meta.Address())
	}, nil
}

// sessionInput reports which of msgs this session received, from its stored
// delivery rows. The row replaces the address check: it is stored once, for the
// recipients the post named when it was written, and it names the session, so a
// rename keeps the post. Its mode is not the input test: a quiet post is still
// read on the session's next turn, so the caller keeps the post's own family
// check (Message.Deliverable).
func sessionInput(rooms *room.Store, meta Meta, msgs []room.Message) (func(room.Message) bool, error) {
	rows, err := inputRows(rooms, meta.RoomID, meta.ID, meta.Address(), postIDs(msgs))
	if err != nil {
		return nil, err
	}
	return func(msg room.Message) bool {
		_, ok := rows[msg.ID]
		return ok
	}, nil
}

func (s *Store) frameOptions(meta Meta, msg room.Message) room.FrameOptions {
	o := room.FrameOptions{Room: meta.Room, Reader: meta.Address(), Reply: s.ReplyAddress(msg)}
	o.Reply, o.ReplyTo = meta.ServiceContract().FrameReply(msg, o.Reply)
	if len(msg.Attachments) > 0 {
		rooms := &room.Store{Root: filepath.Join(filepath.Dir(s.root()), "rooms")}
		o.FilesDir, _ = rooms.FilesDir(meta.RoomID)
	}
	return o
}

// waitForInput keeps one read-only wait across the harness's short polls.
// A timeout without work repeats the wait, never an empty mutation claim.
func (g *turnGate) waitForInput(deadline <-chan time.Time) (bool, error) {
	g.startInputWait()
	select {
	case err := <-g.waiting.done:
		g.waiting.cancel()
		g.waiting = nil
		return err == nil, err
	case <-g.ctx.Done():
		g.cancelInputWait()
		return false, g.ctx.Err()
	case <-deadline:
		return false, nil
	}
}

func (g *turnGate) cancelInputWait() {
	if g.waiting != nil {
		g.waiting.cancel()
		<-g.waiting.done
		g.waiting = nil
	}
}

// Wake is the session's doorbell. The harness can select on readiness or
// claim settlement instead of using a periodic steering timer.
func (g *turnGate) Wake() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.writes.out == nil {
		return nil
	}
	if g.pending != nil {
		return g.pending.wake
	}
	g.startInputWait()
	return g.waiting.wake
}

func (g *turnGate) startInputWait() {
	if g.waiting == nil {
		ctx, cancel := context.WithCancel(g.ctx)
		done := make(chan error, 1)
		wake := make(chan struct{})
		input := InputClaim{Turn: g.turn, Cursor: g.cursor, Depth: g.meta.Depth, ParentSession: g.meta.ParentSession}
		g.waiting = &pendingInputWait{cancel: cancel, done: done, wake: wake}
		go func() {
			for {
				ready, err := g.writes.waitInput(ctx, input)
				if err != nil || ready {
					done <- err
					close(wake)
					return
				}
			}
		}()
	}
}
