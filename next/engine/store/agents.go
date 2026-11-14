package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// AgentRunning is the state of an agent turn that has not ended. An ended one has its turn's outcome.
const AgentRunning = "running"

// Agent is one turn of an in-box agent, filed under the parent turn that asked. Its steps lie in Transcript, from byte
// Offset, in the parent session's transcript files.
type Agent struct {
	ID         contract.AgentID
	Session    contract.SessionID
	Turn       contract.TurnID
	Name       string
	State      string
	Transcript string
	Offset     int64
	Started    time.Time
	Ended      time.Time
	Usage      contract.Usage
}

const agentColumns = "id,session,turn,name,state,transcript,start_offset,started_at,ended_at,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens"

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var started, ended int64
	err := row.Scan(&a.ID, &a.Session, &a.Turn, &a.Name, &a.State, &a.Transcript, &a.Offset, &started, &ended, &a.Usage.Model, &a.Usage.InputTokens, &a.Usage.OutputTokens, &a.Usage.CacheReadTokens, &a.Usage.CacheWriteTokens)
	a.Started = time.UnixMilli(started)
	if ended != 0 {
		a.Ended = time.UnixMilli(ended)
	}
	return a, err
}

// StartAgent records a turn the engine asked a box to run.
func (s *Store) StartAgent(ctx context.Context, a Agent) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO agents(id,session,turn,name,state,started_at) VALUES(?,?,?,?,?,?)", a.ID, a.Session, a.Turn, a.Name, AgentRunning, a.Started.UnixMilli())
		return err
	})
}

// AgentFile records where the agent's turn begins in its session's transcript, once the box has opened it.
func (s *Store) AgentFile(ctx context.Context, session contract.SessionID, id contract.AgentID, transcript string, offset int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE agents SET transcript=?,start_offset=? WHERE id=? AND session=?", transcript, offset, id, session)
		return err
	})
}

// EndAgent records the end of a running agent turn. A turn that already ended keeps its first end.
func (s *Store) EndAgent(ctx context.Context, session contract.SessionID, id contract.AgentID, outcome contract.TurnOutcome, usage contract.Usage, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE agents SET state=?,ended_at=?,model=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=? WHERE id=? AND session=? AND state=?",
			outcome, at.UnixMilli(), usage.Model, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, id, session, AgentRunning)
		return err
	})
}

// Agent reads one agent turn by its ID.
func (s *Store) Agent(ctx context.Context, id contract.AgentID) (Agent, error) {
	return scanAgent(s.Read().QueryRowContext(ctx, "SELECT "+agentColumns+" FROM agents WHERE id=?", id))
}

// LastAgentEnd returns when the session's latest turn of the named agent ended, or the zero time when it has none.
func (s *Store) LastAgentEnd(ctx context.Context, session contract.SessionID, name string) (time.Time, error) {
	var at sql.NullInt64
	err := s.Read().QueryRowContext(ctx, "SELECT MAX(ended_at) FROM agents WHERE session=? AND name=? AND ended_at>0", session, name).Scan(&at)
	if !at.Valid {
		return time.Time{}, err
	}
	return time.UnixMilli(at.Int64), err
}

// TurnDelivery returns one delivery that a turn took, which files an agent's transcript under that turn.
func (s *Store) TurnDelivery(ctx context.Context, session contract.SessionID, turn contract.TurnID) (contract.DeliverySeq, error) {
	var seq contract.DeliverySeq
	err := s.Read().QueryRowContext(ctx, "SELECT seq FROM deliveries WHERE to_kind='session' AND to_id=? AND turn=? ORDER BY seq LIMIT 1", session, turn).Scan(&seq)
	return seq, err
}

// RoomAdvisorProfile is the room's advisor setting: a profile, or none.
func (s *Store) RoomAdvisorProfile(ctx context.Context, room contract.RoomID) (contract.ProfileName, error) {
	var profile contract.ProfileName
	err := s.Read().QueryRowContext(ctx, "SELECT advisor_profile FROM rooms WHERE id=? AND state='open'", room).Scan(&profile)
	return profile, err
}

// SetRoomAdvisorProfile sets the room's advisor setting.
func (s *Store) SetRoomAdvisorProfile(ctx context.Context, room contract.RoomID, profile contract.ProfileName) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE rooms SET advisor_profile=? WHERE id=? AND state='open'", profile, room)
		return err
	})
}

// AgentRange is where the agent's turn lies in its transcript file: from its start to the start of the next agent turn in the same file, or to
// the end of what the engine holds (nil).
func (s *Store) AgentRange(ctx context.Context, a Agent) (TranscriptTurnRange, error) {
	r := TranscriptTurnRange{Turn: a.Turn, Path: a.Transcript, Start: a.Offset}
	var next sql.NullInt64
	err := s.Read().QueryRowContext(ctx, "SELECT MIN(start_offset) FROM agents WHERE session=? AND transcript=? AND start_offset>?", a.Session, a.Transcript, a.Offset).Scan(&next)
	if next.Valid {
		r.End = &next.Int64
	}
	return r, err
}

// RunningAgents lists the session's agent turns that have not ended.
func (s *Store) RunningAgents(ctx context.Context, session contract.SessionID) ([]Agent, error) {
	return queryAll(ctx, s.Read(), func(rows scanner) (Agent, error) { return scanAgent(rows) }, "SELECT "+agentColumns+" FROM agents WHERE session=? AND state=? ORDER BY started_at", session, AgentRunning)
}
