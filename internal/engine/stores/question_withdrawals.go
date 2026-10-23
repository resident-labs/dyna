package stores

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// TODO: legacy/shim remove later — converts retained withdrawal routing.
// migrateQuestionWithdrawals converts early question-withdrawn records once.
// They named the room as sender; the current record replies to the question
// post among its participants. It runs after the question and session stores
// migrate, because the question row names the post and who withdrew it.
func migrateQuestionWithdrawals(dataDir string) error {
	rooms := &room.Store{Root: filepath.Join(dataDir, "rooms")}
	// The question post precedes its withdrawal in the same room log.
	questionPosts := map[string]room.Message{}
	return rooms.MigratePosts("room.posts.question_withdrawals", func(conn *sql.Conn, _, current map[string]json.RawMessage) (bool, error) {
		raw, err := json.Marshal(current)
		if err != nil {
			return false, err
		}
		var m room.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			// Match the shared reader's treatment of invalid message records.
			return false, nil
		}
		if m.QuestionID() != "" {
			questionPosts[m.RoomID+"/"+m.ID] = m
			return false, nil
		}
		if m.Kind != "question-withdrawn" {
			return false, nil
		}
		id, _, _ := strings.Cut(m.Text, " ")
		var body string
		err = conn.QueryRowContext(context.Background(), "SELECT body FROM questions WHERE key=?", m.RoomID+"/"+id).Scan(&body)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var q questions.Question
		if err := json.Unmarshal([]byte(body), &q); err != nil {
			return false, err
		}
		if q.Post == "" {
			return false, nil
		}
		original, ok := questionPosts[m.RoomID+"/"+q.Post]
		if !ok {
			return false, nil
		}
		from, to := original.From, slices.Clone(original.To)
		if q.Withdrawn != nil && q.Withdrawn.By != q.Asker {
			if q.Withdrawn.By == "owner" {
				var body string
				err := conn.QueryRowContext(context.Background(), "SELECT body FROM room_records WHERE id=?", m.RoomID).Scan(&body)
				if err == sql.ErrNoRows {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				var rec room.Record
				if err := json.Unmarshal([]byte(body), &rec); err != nil {
					return false, err
				}
				from = rec.Name
			} else {
				var body string
				err := conn.QueryRowContext(context.Background(), "SELECT meta FROM session_records WHERE id=?", q.Withdrawn.By).Scan(&body)
				if err == sql.ErrNoRows {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				var info subagent.Meta
				if err := json.Unmarshal([]byte(body), &info); err != nil {
					return false, err
				}
				from = info.Address()
			}
			to = append(to, original.From)
		}
		seen := map[string]bool{from: true}
		to = slices.DeleteFunc(to, func(addr string) bool {
			if seen[addr] {
				return true
			}
			seen[addr] = true
			return false
		})
		for key, value := range map[string]any{
			"from": from, "to": to, "kind": "message", "replyTo": q.Post,
			"text": "[WITHDRAWN " + id + "] " + strings.TrimPrefix(m.Text, id+" withdrawn: "),
		} {
			if current[key], err = json.Marshal(value); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}
