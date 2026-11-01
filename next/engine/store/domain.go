package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

var ErrParentClosing = errors.New("parent session is closing")
var ErrSessionClosing = errors.New("session is closing")
var ErrVisualForbidden = errors.New("visual belongs to another room or author")
var ErrVisualNoAuthor = errors.New("visual has no session author")

type VisualSupersededError struct {
	Event   contract.EventID
	Version int
	Seq     int64
}

func (e *VisualSupersededError) Error() string { return "visual has a newer version" }

var ErrParentHostAbsent = errors.New("sameHost requires a parent with an assigned host")

// Session describes the stored session and its box.
type Session struct {
	ID          contract.SessionID
	Room        contract.RoomID
	Name        string
	Repo        contract.RepoName
	Profile     contract.ProfileName
	Box         contract.BoxID
	CloseReason contract.SessionCloseReason
	NewRoomName string // nonempty only for a new room root
	Compose     string // the compose file this session chose when it started, or ""
	// Workflow makes the new session a crew. It is set only to start one: Session does not read it back.
	Workflow *WorkflowRun
}

// RoomExists returns sql.ErrNoRows when the room does not exist.
func (s *Store) RoomExists(ctx context.Context, room contract.RoomID) error {
	var id contract.RoomID
	return s.Read().QueryRowContext(ctx, "SELECT id FROM rooms WHERE id=?", room).Scan(&id)
}

func (s *Store) CreateRoom(ctx context.Context, id contract.RoomID, name string, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES(?,?,?,?,?)", id, name, at.UnixMilli(), 0, 0)
		return err
	})
}

// SessionReplay returns the response saved for a caller's session start key.
func (s *Store) SessionReplay(ctx context.Context, caller contract.Principal, clientID string) (contract.SessionStartResponse, error) {
	return sessionReplay(ctx, s.read, caller, clientID)
}

func sessionReplay(ctx context.Context, q sqlQuery, caller contract.Principal, clientID string) (contract.SessionStartResponse, error) {
	var response contract.SessionStartResponse
	err := q.QueryRowContext(ctx, "SELECT s.id,b.id FROM client_replays r JOIN sessions s ON s.id=r.ref JOIN boxes b ON b.session=s.id WHERE r.method=? AND r.caller_kind=? AND r.caller_id=? AND r.client_id=?", contract.MethodSessionStart, caller.Kind, caller.Session, clientID).Scan(&response.Session, &response.Box)
	return response, err
}

func (s *Store) StartSession(ctx context.Context, session Session, parent contract.SessionID, size contract.BoxSizeName, goal string, event, boxEvent contract.Event, reason string, sameHost bool, clientID string, maxDepth int) (contract.SessionStartResponse, error) {
	response := contract.SessionStartResponse{Session: session.ID, Box: session.Box}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if err := refuseNewWork(ctx, tx, session.Room); err != nil {
			return err
		}
		if clientID != "" {
			previous, err := sessionReplay(ctx, tx, event.From, clientID)
			if err == nil {
				response = previous
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if parent != "" {
			tree, err := loadTree(ctx, tx, session.Room)
			if err != nil {
				return err
			}
			actual, err := tree.ParentFor(event.From, parent)
			if err != nil {
				return &TreeError{Message: err.Error()}
			}
			if actual != parent {
				return &TreeError{Message: "parent changed"}
			}
			if err := checkDepth(tree.Depth(parent)+1, maxDepth); err != nil {
				return err
			}
		}
		if clientID != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES(?,?,?,?,?,?)", contract.MethodSessionStart, event.From.Kind, event.From.Session, clientID, session.ID, event.Time.UnixMilli()); err != nil {
				return err
			}
		}
		var host *contract.HostName
		if sameHost {
			if parent == "" {
				return ErrParentHostAbsent
			}
			var assigned sql.NullString
			if err := tx.QueryRowContext(ctx, "SELECT host FROM boxes WHERE session=?", parent).Scan(&assigned); err != nil {
				return err
			}
			if !assigned.Valid || assigned.String == "" {
				return ErrParentHostAbsent
			}
			name := contract.HostName(assigned.String)
			host = &name
		}
		if parent != "" {
			var state string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=? AND room=?", parent, session.Room).Scan(&state); err != nil {
				return err
			}
			if state == "closing" || state == "closed" {
				return ErrParentClosing
			}
		}
		if session.NewRoomName != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES(?,?,?,?,?)", session.Room, session.NewRoomName, event.Time.UnixMilli(), 0, 0); err != nil {
				return err
			}
		}
		var parentID *contract.SessionID
		if parent != "" {
			parentID = &parent
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,room,name,parent,repo,profile,state,goal,created_at,compose) VALUES(?,?,?,?,?,?,?,?,?,?)", session.ID, session.Room, session.Name, parentID, session.Repo, session.Profile, "open", goal, event.Time.UnixMilli(), session.Compose)
		if err != nil {
			return err
		}
		if session.Workflow != nil {
			if err := insertWorkflow(ctx, tx, session.ID, *session.Workflow, event.Time); err != nil {
				return err
			}
		}
		if started, _ := event.Body.(contract.SessionStarted); goal != "" && parent != "" {
			var number int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM goals WHERE room=?", session.Room).Scan(&number); err != nil {
				return err
			}
			at := event.Time.UnixMilli()
			if _, err := tx.ExecContext(ctx, "INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", session.Room, number, session.ID, goal, started.DoneWhen, contract.GoalActive, at, at); err != nil {
				return err
			}
		}
		if session.NewRoomName != "" {
			g := contract.Grant{ID: contract.GrantID("grant_" + string(session.ID)), Kind: contract.GrantHostExec, Room: session.Room, To: contract.Principal{Kind: contract.PrincipalSession, Session: session.ID}, Hosts: []contract.HostName{contract.AllHosts}, By: contract.Principal{Kind: contract.PrincipalEngine}, Created: event.Time}
			if err := s.InsertGrant(tx, g); err != nil {
				return err
			}
			g = contract.Grant{ID: contract.GrantID("grant_ask_" + string(session.ID)), Kind: contract.GrantAskOwner, Room: session.Room, To: g.To, Hosts: []contract.HostName{}, By: g.By, Created: event.Time}
			if err := s.InsertGrant(tx, g); err != nil {
				return err
			}
			if err := insertDefaultSubscription(ctx, tx, session.Room, session.ID, event.Time); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)", session.Box, session.ID, host, "", size, contract.BoxPending, reason, "", event.Time.UnixMilli(), event.Time.UnixMilli())
		if err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		reply := contract.ReplyChannel{Kind: contract.ReplyNone}
		if parent != "" {
			reply = contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalSession, Session: parent}, Home: contract.Conversation{Kind: contract.ConversationFeed}}
		}
		// A workflow answers whoever ran it, and the owner's crew sits under root only because the owner has no session.
		if session.Workflow != nil && event.From.Kind == contract.PrincipalOwner {
			reply.Target = contract.Principal{Kind: contract.PrincipalOwner}
		}
		if started, _ := event.Body.(contract.SessionStarted); goal != "" || started.Brief != "" {
			err = s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: session.ID}, Surface: contract.SurfaceSession, Reply: reply, Wake: true})
			if err != nil {
				return err
			}
		}
		if host != nil {
			body := boxEvent.Body.(contract.BoxChanged)
			body.Host = *host
			boxEvent.Body = body
		}
		_, err = s.AppendEvent(tx, boxEvent)
		return err
	})
	return response, err
}

func (s *Store) CloseSession(ctx context.Context, id contract.SessionID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closed',closed_at=? WHERE id=? AND closed_at IS NULL", event.Time.UnixMilli(), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		for _, table := range []string{"pr_owners", "issue_owners"} {
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET session=(SELECT parent FROM sessions WHERE id=?) WHERE session=? AND EXISTS (SELECT 1 FROM sessions WHERE id=? AND parent IS NOT NULL)", id, id, id); err != nil {
				return err
			}
		}
		if err := dropSessionSubscriptions(ctx, tx, id); err != nil {
			return err
		}
		var parent contract.SessionID
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(parent,'') FROM sessions WHERE id=?", id).Scan(&parent); err != nil {
			return err
		}
		var unheld []contract.GoalID
		if parent != "" {
			if unheld, err = s.transferGoals(ctx, tx, id, parent, event.Time); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,handoff_from)
SELECT 'session',?,seq,'session','post','','','','',1,'pending',COALESCE(handoff_from,?) FROM deliveries d WHERE d.to_kind='session' AND d.to_id=? AND d.state IN ('pending','taken') AND NOT `+boxNotice+`
ON CONFLICT(to_kind,to_id,seq) DO NOTHING`, parent, id, id)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				if _, err := tx.ExecContext(ctx, "UPDATE boxes SET want='run' WHERE session=? AND want='stop' AND EXISTS (SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.id=boxes.session AND r.paused_at IS NULL)", parent); err != nil {
					return err
				}
				s.wakePending = append(s.wakePending, parent)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM deliveries WHERE to_kind='session' AND to_id=? AND state IN ('pending','taken')", id); err != nil {
				return err
			}
		}
		if body, ok := event.Body.(contract.SessionClosed); ok && len(unheld) > 0 {
			body.Unheld = unheld
			event.Body = body
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil || len(unheld) == 0 {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: parent}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
	})
}

func (s *Store) DeliveryDone(ctx context.Context, session contract.SessionID, seq contract.DeliverySeq) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_kind=? AND to_id=? AND seq=? AND state IN ('taken','read')", contract.PrincipalSession, session, seq)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		return nil
	})
}

var ErrTurnConflict = errors.New("turn output differs from the committed result")

func (s *Store) EndTurn(ctx context.Context, session contract.SessionID, output contract.TurnOutputRequest, event contract.Event, also ...func(*sql.Tx) error) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE turns SET ended_at=?,outcome=?,model=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,shell_jobs=?,sections=NULL WHERE id=? AND session=? AND ended_at IS NULL", event.Time.UnixMilli(), output.Outcome, output.Model, output.Usage.InputTokens, output.Usage.OutputTokens, output.Usage.CacheReadTokens, output.Usage.CacheWriteTokens, output.ShellJobs, output.Turn, session)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			var body []byte
			err := tx.QueryRowContext(ctx, "SELECT e.body FROM events e JOIN sessions s ON e.room=s.room WHERE s.id=? AND e.type='turn.ended' AND json_extract(e.body,'$.turn')=?", session, output.Turn).Scan(&body)
			if err != nil {
				return err
			}
			var previous contract.TurnEnded
			if err := json.Unmarshal(body, &previous); err != nil {
				return err
			}
			current := event.Body.(contract.TurnEnded)
			if previous.Session != current.Session || previous.Turn != current.Turn || previous.Outcome != current.Outcome || previous.Final != current.Final || previous.Usage != current.Usage || previous.Error != current.Error {
				return ErrTurnConflict
			}
			return nil
		}
		retrying := false
		if output.Outcome == contract.TurnCompleted {
			if _, err := tx.ExecContext(ctx, "UPDATE boxes SET failures=0 WHERE session=?", session); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=0,model_retries=0 WHERE id=?", session); err != nil {
				return err
			}
		} else if output.Outcome == contract.TurnFailed && output.Cause == contract.CauseModelFailed {
			var retries int
			if err := tx.QueryRowContext(ctx, "SELECT model_retries FROM sessions WHERE id=?", session).Scan(&retries); err != nil {
				return err
			}
			var retryAt int64
			if retries < 8 {
				delay := 5 * time.Minute * time.Duration(1<<retries)
				if delay > time.Hour {
					delay = time.Hour
				}
				retryAt = event.Time.Add(delay).UnixMilli()
				retries++
				retrying = true
			}
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=?,model_retries=? WHERE id=?", retryAt, retries, session); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=0 WHERE id=?", session); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET shell_jobs=MAX(0,?) WHERE id=?", output.ShellJobs, session); err != nil {
			return err
		}
		// A failed turn hands back every input below, without waking. These inputs still wake the session.
		if output.Outcome != contract.TurnFailed && output.Cause != contract.CauseNotStarted {
			for _, seq := range output.Unread {
				if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='pending',turn=NULL,queue=NULL WHERE to_kind=? AND to_id=? AND seq=? AND turn=? AND state='taken'", contract.PrincipalSession, session, seq, output.Turn); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_kind=? AND to_id=? AND turn=? AND state='taken'", contract.PrincipalSession, session, output.Turn); err != nil {
			return err
		}
		inputs, posts, err := s.turnReplies(ctx, tx, event.Room, session, output.Turn)
		if err != nil {
			return err
		}
		// A workflow's failure is its result, and running the script again would end the same way.
		if (output.Outcome == contract.TurnFailed && output.Cause != contract.CauseOutputLimit && output.Cause != contract.CauseWorkflowFailed) || output.Cause == contract.CauseNotStarted {
			if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='pending',turn=NULL,queue=NULL,wake=0 WHERE to_kind=? AND to_id=? AND turn=? AND state IN ('taken','read')", contract.PrincipalSession, session, output.Turn); err != nil {
				return err
			}
		}
		ended, err := s.endPosts(ctx, tx, event.Room, session, output)
		if err != nil {
			return err
		}
		endIDs := make([]contract.EventID, len(ended))
		for i, post := range ended {
			endIDs[i] = post.ID
		}
		body := event.Body.(contract.TurnEnded)
		replyPosts := room.PostsForReplies(inputs, posts, session)
		// A crew's posts are its script's and its workers'. None of them answers the run.
		var crew bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_runs WHERE session=?)", session).Scan(&crew); err != nil {
			return err
		}
		if crew {
			replyPosts = nil
		}
		body.Replies = room.FinalReplies(inputs, replyPosts, endIDs)
		event.Body = body
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if output.Outcome != contract.TurnStopped && !retrying && len(body.Replies) == 0 && !slices.ContainsFunc(posts, func(post contract.Event) bool {
			return post.From.Kind == contract.PrincipalSession && post.From.Session == session
		}) {
			if err := markSilent(ctx, tx, session, output.Turn); err != nil {
				return err
			}
		}
		for _, reply := range body.Replies {
			if reply.Reply.Target.Kind != contract.PrincipalSession {
				continue
			}
			if output.Outcome == contract.TurnFailed {
				if err := markFailureTold(ctx, tx, session); err != nil {
					return err
				}
			}
			d := contract.Delivery{To: reply.Reply.Target, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyPost, Target: event.From, Home: reply.Reply.Home}, Wake: reply.Posted == ""}
			if len(ended) > 0 {
				if err := s.deliverEndPost(ctx, tx, ended, reply.Posted, d); err != nil {
					return err
				}
				continue
			}
			if err := s.InsertDelivery(ctx, tx, seq, event.Body, d); err != nil {
				return err
			}
		}
		for _, step := range also {
			if err := step(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Session(ctx context.Context, id contract.SessionID) (Session, error) {
	var session Session
	err := s.Read().QueryRowContext(ctx, "SELECT s.id,s.room,s.name,s.repo,s.profile,b.id,s.close_reason,s.compose FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=? AND s.closed_at IS NULL", id).Scan(&session.ID, &session.Room, &session.Name, &session.Repo, &session.Profile, &session.Box, &session.CloseReason, &session.Compose)
	return session, err
}

func (s *Store) RoomByName(ctx context.Context, name contract.RoomName) (contract.RoomID, error) {
	var id contract.RoomID
	err := s.Read().QueryRowContext(ctx, "SELECT id FROM rooms WHERE name=? AND state='open'", name).Scan(&id)
	return id, err
}

func (s *Store) SessionByName(ctx context.Context, room contract.RoomID, name string) (Session, error) {
	var session Session
	err := s.Read().QueryRowContext(ctx, "SELECT s.id,s.room,s.name,s.repo,s.profile,b.id FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.room=? AND s.name=? AND s.closed_at IS NULL", room, name).Scan(&session.ID, &session.Room, &session.Name, &session.Repo, &session.Profile, &session.Box)
	return session, err
}

// InsertDelivery adds a delivery within the active store write transaction.
func (s *Store) InsertDelivery(ctx context.Context, tx *sql.Tx, seq int64, body contract.EventBody, d contract.Delivery) error {
	if _, err := room.RenderDelivery(body); err != nil {
		return err
	}
	home := string(d.Reply.Home.Kind)
	if d.Reply.Home.Kind == contract.ConversationDM {
		home += ":" + string(d.Reply.Home.Session)
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state) VALUES(?,?,?,?,?,?,?,?,?,?,?)", d.To.Kind, d.To.Session, seq, d.Surface, d.Reply.Kind, d.Reply.Target.Kind, d.Reply.Target.Session, home, d.Reply.ReplyTo, d.Wake, "pending")
	if err != nil {
		return err
	}
	if d.Wake && d.To.Kind == contract.PrincipalSession {
		_, err = tx.ExecContext(ctx, `UPDATE boxes SET want='run' WHERE session=? AND want='stop' AND EXISTS (SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.id=boxes.session AND r.paused_at IS NULL)`, d.To.Session)
		if err != nil {
			return err
		}
	}
	if d.To.Kind == contract.PrincipalSession {
		s.wakePending = append(s.wakePending, d.To.Session)
	}
	return err
}

func (s *Store) Post(ctx context.Context, event contract.Event, deliveries []contract.Delivery) (int64, error) {
	_, seq, err := s.PostOnce(ctx, event, deliveries, "", "")
	return seq, err
}

const postByClientID = "SELECT seq,id FROM events WHERE room=? AND from_kind=? AND from_id=? AND client_id=?"

// PostByClientID returns the post this author already made in the room with this client ID, if any.
func (s *Store) PostByClientID(ctx context.Context, room contract.RoomID, from contract.Principal, clientID string) (contract.EventID, int64, bool, error) {
	var id contract.EventID
	var seq int64
	err := s.Read().QueryRowContext(ctx, postByClientID, room, from.Kind, from.Session, clientID).Scan(&seq, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return id, seq, err == nil, err
}

// PostOnce appends a post. When the same author already posted the same client ID in the room, it appends
// nothing and returns that first post.
func (s *Store) PostOnce(ctx context.Context, event contract.Event, deliveries []contract.Delivery, visualUpdate, visualReply contract.EventID) (contract.EventID, int64, error) {
	var seq int64
	id := event.ID
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if post, ok := event.Body.(contract.PostCreated); ok && post.ClientID != "" {
			err = tx.QueryRowContext(ctx, postByClientID, event.Room, event.From.Kind, event.From.Session, post.ClientID).Scan(&seq, &id)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := refuseNewWork(ctx, tx, event.Room); err != nil {
			return err
		}
		if visualReply != "" {
			var visualID contract.EventID
			var version, current int
			var authorKind contract.PrincipalKind
			var author contract.SessionID
			err := tx.QueryRowContext(ctx, `SELECT json_extract(a.value,'$.visual.id'), json_extract(a.value,'$.visual.version')
				FROM events e, json_each(e.body,'$.attachments') a
				WHERE e.id=? AND e.room=? AND e.type='post.created' AND json_type(a.value,'$.visual')='object'`, visualReply, event.Room).Scan(&visualID, &version)
			if err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "SELECT version,author_kind,author_id FROM visuals WHERE id=? AND room=?", visualID, event.Room).Scan(&current, &authorKind, &author); err != nil {
				return err
			}
			if version != current {
				var latest contract.EventID
				var seq int64
				err := tx.QueryRowContext(ctx, `SELECT e.id, e.seq FROM events e, json_each(e.body,'$.attachments') a
					WHERE e.room=? AND e.type='post.created' AND json_extract(a.value,'$.visual.id')=? AND json_extract(a.value,'$.visual.version')=?
					ORDER BY e.seq DESC LIMIT 1`, event.Room, visualID, current).Scan(&latest, &seq)
				if err != nil {
					return err
				}
				return &VisualSupersededError{Event: latest, Version: current, Seq: seq}
			}
			if authorKind != contract.PrincipalSession || author == "" {
				return ErrVisualNoAuthor
			}
			post := event.Body.(contract.PostCreated)
			target := contract.Principal{Kind: contract.PrincipalSession, Session: author}
			post.Mentions = []contract.Principal{target}
			post.ReplyTo = visualReply
			event.Body = post
			deliveries = []contract.Delivery{{To: target, Surface: contract.SurfaceCard, Wake: true,
				Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: event.From, Home: post.Home, ReplyTo: visualReply}}}
		}
		post, isPost := event.Body.(contract.PostCreated)
		if isPost {
			for i := range post.Attachments {
				v := post.Attachments[i].Visual
				if v == nil {
					continue
				}
				v.ID, v.Version = event.ID, 1
				if visualUpdate != "" {
					v.ID = visualUpdate
					var room, kind, author string
					if err := tx.QueryRowContext(ctx, "SELECT room,author_kind,author_id,version FROM visuals WHERE id=?", v.ID).Scan(&room, &kind, &author, &v.Version); err != nil {
						return err
					}
					if room != string(event.Room) || (event.From.Kind != contract.PrincipalOwner && (kind != string(event.From.Kind) || author != string(event.From.Session))) {
						return ErrVisualForbidden
					}
					v.Version++
					if _, err := tx.ExecContext(ctx, "UPDATE visuals SET version=? WHERE id=?", v.Version, v.ID); err != nil {
						return err
					}
				} else {
					if _, err := tx.ExecContext(ctx, "INSERT INTO visuals(id,room,author_kind,author_id,version) VALUES(?,?,?,?,1)", v.ID, event.Room, event.From.Kind, event.From.Session); err != nil {
						return err
					}
				}
			}
			event.Body = post
		}
		for _, delivery := range deliveries {
			if delivery.To.Kind != contract.PrincipalSession {
				continue
			}
			var open bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=? AND s.closed_at IS NULL AND b.want NOT IN ('close','discard'))", delivery.To.Session).Scan(&open); err != nil {
				return err
			}
			if !open {
				return ErrSessionClosing
			}
		}
		if post, ok := event.Body.(contract.PostCreated); ok && event.From.Kind == contract.PrincipalSession {
			err := tx.QueryRowContext(ctx, "SELECT id FROM turns WHERE session=? AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1", event.From.Session).Scan(&post.Turn)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			event.Body = post
		}
		seq, err = s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if post, ok := event.Body.(contract.PostCreated); ok {
			for _, file := range post.Attachments {
				if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_files(seq,sha256,media) VALUES(?,?,?)", seq, file.SHA256, file.Media); err != nil {
					return err
				}
				if file.Visual != nil {
					refs := []string{file.Visual.Banner, file.Visual.Thumbnail}
					for _, shot := range file.Visual.Shots {
						refs = append(refs, shot.File)
					}
					for _, ref := range refs {
						if ref != "" {
							if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_files(seq,sha256,media) VALUES(?,?,?)", seq, ref, "image/webp"); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		for _, delivery := range deliveries {
			if err := s.InsertDelivery(ctx, tx, seq, event.Body, delivery); err != nil {
				return err
			}
		}
		return nil
	})
	return id, seq, err
}

// FeedInput describes a delivery selected for a box channel.
type FeedInput struct {
	Seq         contract.DeliverySeq
	Surface     contract.Surface
	Reply       contract.ReplyChannel
	Queue       *contract.TurnQueue
	HandoffFrom contract.SessionID
}

// Dispatch atomically assigns pending inputs to the active turn, or starts one.
// replay includes previously taken inputs for a new channel connection.
// Dispatch also returns the session's profile, read in the same transaction as its inputs.
// DrainDispatch prevents further turn dispatches and waits for any active dispatch.
func (s *Store) DrainDispatch() {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.draining = true
}

func (s *Store) Dispatch(ctx context.Context, session contract.SessionID, turn contract.TurnID, started contract.Event, replay bool) (contract.TurnID, []FeedInput, contract.ProfileName, error) {
	s.dispatchMu.RLock()
	defer s.dispatchMu.RUnlock()
	if s.draining {
		return "", nil, "", nil
	}
	var hasDeliveries bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deliveries WHERE to_kind=? AND to_id=? AND (state IN ('pending','taken') OR (state='read' AND queue IS NULL AND ?)))`, contract.PrincipalSession, session, replay).Scan(&hasDeliveries)
	if err != nil || !hasDeliveries {
		return "", nil, "", err
	}
	var active contract.TurnID
	var inputs []FeedInput
	var profile contract.ProfileName
	var paused bool
	err = s.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT s.profile,r.paused_at IS NOT NULL FROM sessions s JOIN boxes b ON b.session=s.id JOIN rooms r ON r.id=s.room WHERE s.id=? AND s.state='open' AND s.closed_at IS NULL AND b.want='run' AND b.state='ready'`, session).Scan(&profile, &paused)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT id FROM turns WHERE session=? AND ended_at IS NULL", session).Scan(&active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT d.seq,d.surface,d.state,d.queue,e.id,d.turn,d.reply_kind,d.reply_target_kind,d.reply_target_id,d.reply_home,d.reply_to,d.wake,COALESCE(d.handoff_from,'') FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_kind=? AND d.to_id=? AND (d.state IN ('pending','taken') OR (d.state='read' AND d.turn=? AND d.queue IS NULL AND ?)) ORDER BY d.seq", contract.PrincipalSession, session, active, replay)
		if err != nil {
			return err
		}
		type item struct {
			input FeedInput
			state string
			queue sql.NullString
			event contract.EventID
			turn  sql.NullString
			home  string
			wake  bool
		}
		var items []item
		for rows.Next() {
			var v item
			if err := rows.Scan(&v.input.Seq, &v.input.Surface, &v.state, &v.queue, &v.event, &v.turn, &v.input.Reply.Kind, &v.input.Reply.Target.Kind, &v.input.Reply.Target.Session, &v.home, &v.input.Reply.ReplyTo, &v.wake, &v.input.HandoffFrom); err != nil {
				rows.Close()
				return err
			}
			kind, session, _ := strings.Cut(v.home, ":")
			v.input.Reply.Home = contract.Conversation{Kind: contract.ConversationKind(kind), Session: contract.SessionID(session)}
			if (!paused || (replay && v.turn.Valid && contract.TurnID(v.turn.String) == active)) && (active == "" || v.wake || v.turn.Valid) {
				items = append(items, v)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if active == "" {
			for _, item := range items {
				if item.wake {
					active = turn
					break
				}
			}
			if active == "" {
				return nil
			}
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=0 WHERE id=?", session); err != nil {
				return err
			}
			var ids []contract.EventID
			for _, v := range items {
				ids = append(ids, v.event)
			}
			started.Body = contract.TurnStarted{Session: session, Turn: active, Inputs: ids}
			seq, err := s.AppendEvent(tx, started)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,started_seq,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES(?,?,?,?,?,?,?,?,?,?)", active, session, started.Time.UnixMilli(), seq, "", "", 0, 0, 0, 0)
			if err != nil {
				return err
			}
		}
		for _, v := range items {
			assigned := v.turn.Valid && contract.TurnID(v.turn.String) == active
			if assigned && !replay {
				continue
			}
			if !assigned {
				var queue *contract.TurnQueue
				if active != turn {
					q := contract.QueueSteer
					queue = &q
				}
				_, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='taken',turn=?,queue=? WHERE to_kind=? AND to_id=? AND seq=? AND state IN ('pending','taken')", active, queue, contract.PrincipalSession, session, v.input.Seq)
				if err != nil {
					return err
				}
				v.input.Queue = queue
			} else if v.queue.Valid {
				q := contract.TurnQueue(v.queue.String)
				v.input.Queue = &q
			}
			inputs = append(inputs, v.input)
		}
		return nil
	})
	return active, inputs, profile, err
}

// SessionHasTurns reports whether a box ever ran a turn for the session. A session without turns has no work to archive.
func (s *Store) SessionHasTurns(ctx context.Context, session contract.SessionID) (bool, error) {
	var has bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM turns WHERE session=?)", session).Scan(&has)
	return has, err
}
