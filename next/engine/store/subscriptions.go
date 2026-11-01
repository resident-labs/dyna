package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// defaultEventsJSON lists what wakes root by default: engine notices and repository events (spec 7.2, 8.5).
// An owned repository event is left to its owner, and a type that nothing emits yet matches nothing.
const defaultEventsJSON = `["notice.posted","pr.opened","pr.merged","pr.closed","ci.finished","repo.main_updated"]`

var defaultEvents = []contract.EventType{"notice.posted", "pr.opened", "pr.merged", "pr.closed", "ci.finished", "repo.main_updated"}

// roomSubscriptions selects a room's subscriptions: its own, its sessions', and a machine one that wakes a session here.
const roomSubscriptions = `room=?1 OR session IN (SELECT id FROM sessions WHERE room=?1) OR (wake_kind='session' AND wake_id IN (SELECT id FROM sessions WHERE room=?1))`

// subscriptionCursor is the meta key of the last event dispatch has seen.
const subscriptionCursor = "subscription_seq"

// ErrSubscriptionTarget refuses a subscription for a session that has closed.
var ErrSubscriptionTarget = errors.New("subscription session is closed")

// Subscription is a stored subscription with its schedule and its last result.
type Subscription struct {
	contract.Subscription
	Disabled  bool
	Default   bool
	DueAt     time.Time // zero when nothing is due
	HoldUntil time.Time // the end of a failure backoff
	Failures  int
	LastAt    time.Time
	Last      contract.HookResult
	LastSeq   int64 // the last hook.due event of a scheduled Wake
	Running   bool
	Held      bool // its room or its target's room is paused or going away
}

// HookRun is one Run in progress. The row exists from placement until its result is stored.
type HookRun struct {
	ID           contract.BoxID
	Subscription contract.SubscriptionID
	Room         contract.RoomID
	Host         contract.HostName
	Size         contract.BoxSizeName
	StartedAt    time.Time
}

// DefaultSubscriptionID names a room's own subscription for root, so a backfill and a new room agree.
func DefaultSubscriptionID(id contract.RoomID) contract.SubscriptionID {
	return contract.SubscriptionID("sub_" + string(id))
}

func millis(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func nullText[T ~string](value T) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}

func insertDefaultSubscription(ctx context.Context, tx *sql.Tx, id contract.RoomID, root contract.SessionID, at time.Time) error {
	wake := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	return insertSubscription(ctx, tx, contract.Subscription{ID: DefaultSubscriptionID(id), Scope: contract.ScopeRoom, Room: id, On: defaultEvents, Wake: &wake}, at)
}

func insertSubscription(ctx context.Context, tx *sql.Tx, sub contract.Subscription, at time.Time) error {
	var wakeKind contract.PrincipalKind
	var wakeID contract.SessionID
	if sub.Wake != nil {
		wakeKind, wakeID = sub.Wake.Kind, sub.Wake.Session
	}
	var image string
	var size contract.BoxSizeName
	if sub.Run != nil {
		image, size = sub.Run.Image, sub.Run.Size
	}
	var due int64
	if sub.Every > 0 {
		due = at.UnixMilli() + int64(sub.Every)
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO subscriptions(id,scope,room,session,every_ms,wake_kind,wake_id,run_image,run_size,created_at,due_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
		sub.ID, sub.Scope, nullText(sub.Room), nullText(sub.Session), sub.Every, wakeKind, wakeID, image, size, at.UnixMilli(), due)
	if err != nil {
		return err
	}
	for _, kind := range sub.On {
		if _, err := tx.ExecContext(ctx, "INSERT INTO subscription_events(sub,event_type) VALUES(?,?)", sub.ID, kind); err != nil {
			return err
		}
	}
	if sub.Run == nil {
		return nil
	}
	for pos, arg := range sub.Run.Command {
		if _, err := tx.ExecContext(ctx, "INSERT INTO subscription_args(sub,pos,arg) VALUES(?,?,?)", sub.ID, pos, arg); err != nil {
			return err
		}
	}
	for _, file := range sub.Run.Files {
		if _, err := tx.ExecContext(ctx, "INSERT INTO subscription_files(sub,path,body) VALUES(?,?,?)", sub.ID, file.Path, file.Body); err != nil {
			return err
		}
	}
	return nil
}

// AddSubscription stores a checked subscription. A schedule first fires one period from now.
func (s *Store) AddSubscription(ctx context.Context, sub contract.Subscription, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if sub.Room != "" {
			if err := refuseNewWork(ctx, tx, sub.Room); err != nil {
				return err
			}
		}
		for _, session := range []contract.SessionID{sub.Session, wakeTarget(sub)} {
			if session == "" {
				continue
			}
			var open bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND closed_at IS NULL)", session).Scan(&open); err != nil {
				return err
			}
			if !open {
				return ErrSubscriptionTarget
			}
		}
		return insertSubscription(ctx, tx, sub, at)
	})
}

func wakeTarget(sub contract.Subscription) contract.SessionID {
	if sub.Wake == nil {
		return ""
	}
	return sub.Wake.Session
}

const subscriptionSelect = `SELECT s.id,s.scope,COALESCE(s.room,''),COALESCE(s.session,''),s.every_ms,s.wake_kind,s.wake_id,s.run_image,s.run_size,s.enabled,s.due_at,s.hold_until,s.failures,s.last_at,s.last_result,s.last_seq,
EXISTS(SELECT 1 FROM subscription_runs u WHERE u.sub=s.id),
EXISTS(SELECT 1 FROM rooms r WHERE (r.paused_at IS NOT NULL OR r.state!='open') AND r.id IN (s.room,(SELECT t.room FROM sessions t WHERE t.id=s.wake_id)))
FROM subscriptions s `

func loadSubscriptions(ctx context.Context, q sqlQuery, where string, args ...any) ([]Subscription, error) {
	rows, err := q.QueryContext(ctx, subscriptionSelect+where, args...)
	if err != nil {
		return nil, err
	}
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		var wakeKind contract.PrincipalKind
		var wakeID contract.SessionID
		var image string
		var size contract.BoxSizeName
		var enabled bool
		var due, hold, last int64
		if err := rows.Scan(&sub.ID, &sub.Scope, &sub.Room, &sub.Session, &sub.Every, &wakeKind, &wakeID, &image, &size, &enabled, &due, &hold, &sub.Failures, &last, &sub.Last, &sub.LastSeq, &sub.Running, &sub.Held); err != nil {
			rows.Close()
			return nil, err
		}
		if wakeKind != "" {
			sub.Wake = &contract.Principal{Kind: wakeKind, Session: wakeID}
		} else {
			sub.Run = &contract.RunSpec{Image: image, Size: size}
		}
		sub.Disabled, sub.DueAt, sub.HoldUntil, sub.LastAt = !enabled, fromMillis(due), fromMillis(hold), fromMillis(last)
		sub.Default = sub.ID == DefaultSubscriptionID(sub.Room)
		out = append(out, sub)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if err := loadSubscriptionParts(ctx, q, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func loadSubscriptionParts(ctx context.Context, q sqlQuery, sub *Subscription) error {
	var err error
	if sub.On, err = listIDs[contract.EventType](ctx, q, "SELECT event_type FROM subscription_events WHERE sub=? ORDER BY event_type", sub.ID); err != nil {
		return err
	}
	if sub.Run == nil {
		return nil
	}
	if sub.Run.Command, err = listIDs[string](ctx, q, "SELECT arg FROM subscription_args WHERE sub=? ORDER BY pos", sub.ID); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, "SELECT path,body FROM subscription_files WHERE sub=? ORDER BY path", sub.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var file contract.RunFile
		if err := rows.Scan(&file.Path, &file.Body); err != nil {
			return err
		}
		sub.Run.Files = append(sub.Run.Files, file)
	}
	return rows.Err()
}

// SubscriptionByID finds one subscription.
func (s *Store) SubscriptionByID(ctx context.Context, id contract.SubscriptionID) (Subscription, error) {
	subs, err := loadSubscriptions(ctx, s.Read(), "WHERE s.id=?", id)
	if err != nil {
		return Subscription{}, err
	}
	if len(subs) == 0 {
		return Subscription{}, ErrNotFound
	}
	return subs[0], nil
}

// RoomSubscriptions lists a room's subscriptions, its sessions' included. No room lists the machine's.
func (s *Store) RoomSubscriptions(ctx context.Context, id contract.RoomID) ([]Subscription, error) {
	if id == "" {
		return loadSubscriptions(ctx, s.Read(), "WHERE s.scope='machine' ORDER BY s.id")
	}
	return loadSubscriptions(ctx, s.Read(), "WHERE s.room=? ORDER BY s.id", id)
}

func deleteSubscription(ctx context.Context, tx *sql.Tx, id contract.SubscriptionID) error {
	for _, statement := range []string{
		"DELETE FROM subscription_events WHERE sub=?",
		"DELETE FROM subscription_args WHERE sub=?",
		"DELETE FROM subscription_files WHERE sub=?",
		"DELETE FROM subscriptions WHERE id=?",
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return err
		}
	}
	return nil
}

// RemoveSubscription deletes a subscription. A Run in progress stops on the next watch.
func (s *Store) RemoveSubscription(ctx context.Context, id contract.SubscriptionID) error {
	return s.Write(ctx, func(tx *sql.Tx) error { return deleteSubscription(ctx, tx, id) })
}

// dropSessionSubscriptions ends what belongs to a closing session or can only wake it.
func dropSessionSubscriptions(ctx context.Context, tx *sql.Tx, session contract.SessionID) error {
	ids, err := listIDs[contract.SubscriptionID](ctx, tx, "SELECT id FROM subscriptions WHERE session=? OR (wake_kind='session' AND wake_id=?)", session, session)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := deleteSubscription(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// SetSubscriptionEnabled turns a subscription on or off. Either way it ends a backoff, so enabling retries a failing Run now.
func (s *Store) SetSubscriptionEnabled(ctx context.Context, id contract.SubscriptionID, enabled bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE subscriptions SET enabled=?,hold_until=0 WHERE id=?", enabled, id)
		return err
	})
}

// subscriptionMatches says whether an event fires a subscription. dm marks an event held in a DM, which no
// subscription sees: a delivery to a third session would break the DM.
func subscriptionMatches(sub Subscription, event contract.Event, dm bool) bool {
	if dm || sub.Held || sub.Disabled {
		return false
	}
	if sub.Scope != contract.ScopeMachine && event.Room != sub.Room {
		return false
	}
	kind := event.Body.EventType()
	found := false
	for _, want := range sub.On {
		found = found || want == kind
	}
	if !found {
		return false
	}
	if owned, ok := event.Body.(interface{ OwnedBy() contract.SessionID }); ok && sub.Default && owned.OwnedBy() != "" {
		return false
	}
	// An issue.changed with no news only keeps rows current, so it has nothing to say.
	issues, isIssues := event.Body.(contract.IssueChanged)
	if isIssues && !issues.Waking() {
		return false
	}
	if sub.Wake == nil {
		return true
	}
	// A session does not wake on its own events, or a turn's end would start the next turn forever.
	if event.From.Kind == contract.PrincipalSession && event.From.Session == sub.Wake.Session {
		return false
	}
	// The issue's owner already has the pass in its one delivery, on this event or on the pr.changed beside it.
	if isIssues && issues.Session == sub.Wake.Session {
		return false
	}
	_, err := room.RenderDelivery(event.Body)
	return err == nil
}

// DispatchEvents fires subscriptions on up to limit committed events after the cursor, in one write with
// the cursor, so a restart repeats nothing and misses nothing. It returns how many events it read.
func (s *Store) DispatchEvents(ctx context.Context, limit int) (int, error) {
	var cursor, last int64
	if err := s.Read().QueryRowContext(ctx, "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key=?),0),COALESCE((SELECT MAX(seq) FROM events),0)", subscriptionCursor).Scan(&cursor, &last); err != nil {
		return 0, err
	}
	if cursor == last {
		return 0, nil
	}
	count := 0
	err := s.Write(ctx, func(tx *sql.Tx) error {
		count = 0
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key=?),0),COALESCE((SELECT MAX(seq) FROM events),0)", subscriptionCursor).Scan(&cursor, &last); err != nil {
			return err
		}
		// Deleting a room can take the newest events, and their sequence numbers come back.
		cursor = min(cursor, last)
		subs, err := loadSubscriptions(ctx, tx, "WHERE s.enabled=1 AND EXISTS(SELECT 1 FROM subscription_events e WHERE e.sub=s.id)")
		if err != nil {
			return err
		}
		next := last
		if len(subs) > 0 {
			rows, err := tx.QueryContext(ctx, "SELECT seq,id,at,room,type,from_kind,from_id,body,home IS NOT NULL FROM events WHERE seq>? AND type IN (SELECT e.event_type FROM subscription_events e JOIN subscriptions s ON s.id=e.sub WHERE s.enabled=1) ORDER BY seq LIMIT ?", cursor, limit)
			if err != nil {
				return err
			}
			var events []contract.Event
			var inDM []bool
			for rows.Next() {
				var dm bool
				event, err := scanEvent(rows, &dm)
				if err != nil {
					rows.Close()
					return err
				}
				events, inDM = append(events, event), append(inDM, dm)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for i, event := range events {
				for _, sub := range subs {
					if !subscriptionMatches(sub, event, inDM[i]) {
						continue
					}
					if err := s.fire(ctx, tx, sub, event); err != nil {
						return err
					}
				}
			}
			count = len(events)
			if count == limit {
				next = events[count-1].Seq
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", subscriptionCursor, strconv.FormatInt(next, 10))
		return err
	})
	return count, err
}

// fire wakes a target on an event, or marks a Run due. Many events before a Run starts make one Run.
func (s *Store) fire(ctx context.Context, tx *sql.Tx, sub Subscription, event contract.Event) error {
	if sub.Wake == nil {
		_, err := tx.ExecContext(ctx, "UPDATE subscriptions SET due_at=? WHERE id=? AND due_at=0", event.Time.UnixMilli(), sub.ID)
		return err
	}
	var delivered bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM deliveries WHERE to_kind=? AND to_id=? AND seq=?)", sub.Wake.Kind, sub.Wake.Session, event.Seq).Scan(&delivered); err != nil || delivered {
		return err
	}
	return s.InsertDelivery(ctx, tx, event.Seq, event.Body, contract.Delivery{To: *sub.Wake, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
}

// DueSubscriptions lists what can fire now: due, enabled, past any backoff, with no Run in progress and in a
// room that is neither paused nor going away. A due subscription in a paused room keeps its time, so it fires
// once when the room resumes.
func (s *Store) DueSubscriptions(ctx context.Context, now time.Time) ([]Subscription, error) {
	subs, err := loadSubscriptions(ctx, s.Read(), "WHERE s.enabled=1 AND s.due_at>0 AND s.due_at<=?1 AND s.hold_until<=?1 ORDER BY s.due_at,s.id", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	var due []Subscription
	for _, sub := range subs {
		if !sub.Held && !sub.Running {
			due = append(due, sub)
		}
	}
	return due, nil
}

// nextDue is when a subscription that fired at now fires again. Only a schedule has a next time.
func nextDue(sub Subscription, now time.Time) int64 {
	if sub.Every == 0 {
		return 0
	}
	return now.UnixMilli() + int64(sub.Every)
}

// FireScheduledWake delivers a schedule's wake and moves the schedule. A target that has not yet taken the
// last one gets no second: wakes do not stack up.
func (s *Store) FireScheduledWake(ctx context.Context, sub Subscription, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE subscriptions SET due_at=? WHERE id=? AND enabled=1 AND due_at=?", nextDue(sub, event.Time), sub.ID, millis(sub.DueAt))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		var waiting bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM deliveries WHERE to_kind=? AND to_id=? AND seq=? AND state='pending')", sub.Wake.Kind, sub.Wake.Session, sub.LastSeq).Scan(&waiting); err != nil || waiting {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if err := s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: *sub.Wake, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE subscriptions SET last_seq=?,last_at=? WHERE id=?", seq, event.Time.UnixMilli(), sub.ID)
		return err
	})
}

// BeginRun claims a due subscription for one Run. The unique row is what stops two Runs of a subscription
// overlapping, and the check on due_at is what stops one firing being started twice.
func (s *Store) BeginRun(ctx context.Context, sub Subscription, run HookRun) (bool, error) {
	begun := false
	err := s.Write(ctx, func(tx *sql.Tx) error {
		begun = false
		result, err := tx.ExecContext(ctx, "UPDATE subscriptions SET due_at=? WHERE id=? AND enabled=1 AND due_at=?", nextDue(sub, run.StartedAt), sub.ID, millis(sub.DueAt))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		result, err = tx.ExecContext(ctx, "INSERT INTO subscription_runs(id,sub,room,host,size,started_at) VALUES(?,?,?,?,?,?) ON CONFLICT(sub) DO NOTHING", run.ID, run.Subscription, nullText(run.Room), run.Host, run.Size, run.StartedAt.UnixMilli())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		begun = n == 1
		return err
	})
	return begun, err
}

// ActiveRuns lists the Runs in progress.
func (s *Store) ActiveRuns(ctx context.Context) ([]HookRun, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,sub,COALESCE(room,''),host,size,started_at FROM subscription_runs ORDER BY started_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HookRun
	for rows.Next() {
		var run HookRun
		var at int64
		if err := rows.Scan(&run.ID, &run.Subscription, &run.Room, &run.Host, &run.Size, &at); err != nil {
			return nil, err
		}
		run.StartedAt = time.UnixMilli(at).UTC()
		out = append(out, run)
	}
	return out, rows.Err()
}

// HookOutcome is what one Run leaves on its subscription.
type HookOutcome struct {
	Failures  int
	HoldUntil time.Time
	Result    contract.HookResult
}

// FinishRun stores a Run's result and its events in one write. A second call for the same Run does nothing.
func (s *Store) FinishRun(ctx context.Context, run HookRun, outcome HookOutcome, events ...contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM subscription_runs WHERE id=?", run.ID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		at := events[0].Time.UnixMilli()
		if _, err := tx.ExecContext(ctx, "UPDATE subscriptions SET failures=?,hold_until=?,last_at=?,last_result=? WHERE id=?", outcome.Failures, millis(outcome.HoldUntil), at, outcome.Result, run.Subscription); err != nil {
			return err
		}
		for _, event := range events {
			if _, err := s.AppendEvent(tx, event); err != nil {
				return err
			}
		}
		return nil
	})
}

// RoomLimits reads a room's limits. Zero means no limit.
func (s *Store) RoomLimits(ctx context.Context, id contract.RoomID) (contract.RoomLimits, error) {
	var limits contract.RoomLimits
	err := s.Read().QueryRowContext(ctx, "SELECT cpus,mem_bytes FROM rooms WHERE id=?", id).Scan(&limits.CPUs, &limits.MemBytes)
	return limits, err
}
