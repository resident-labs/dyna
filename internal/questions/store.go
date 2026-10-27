// Package questions stores async questions in each room.
package questions

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/sessionlife"
)

type Answer struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
	// Human marks an owner answer a person gave, like room.Message.Human.
	Human  bool   `json:"human,omitempty"`
	Choice string `json:"choice,omitempty"`
	Text   string `json:"text,omitempty"`
	// Source is room.SourceCard when an HTML card wrote the answer and the
	// owner confirmed it.
	Source string `json:"source,omitempty"`
}
type Withdraw struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
	// Human marks an owner withdrawal a person made, like room.Message.Human.
	Human  bool   `json:"human,omitempty"`
	Reason string `json:"reason"`
}

// AskerKind says what kind of sender asked a question, so answer delivery
// does not parse the Asker address.
type AskerKind string

const (
	AskerOwner   AskerKind = "owner"
	AskerSession AskerKind = "session"
	AskerRun     AskerKind = "run"
)

func (k AskerKind) valid() bool { return k == AskerOwner || k == AskerSession || k == AskerRun }

type Question struct {
	ID    string `json:"id"`
	Room  string `json:"room"`
	Asker string `json:"asker"`
	// AskerKind is stored with the question and set when it is asked.
	AskerKind AskerKind `json:"askerKind"`
	// Human marks an owner question a person asked, like room.Message.Human.
	Human           bool      `json:"human,omitempty"`
	To              string    `json:"to"`
	Text            string    `json:"text"`
	Options         []string  `json:"options"`
	Meanwhile       string    `json:"meanwhile"`
	Post            string    `json:"post"`
	State           string    `json:"state"`
	Answer          *Answer   `json:"answer,omitempty"`
	Withdrawn       *Withdraw `json:"withdrawn,omitempty"`
	AskedAt         time.Time `json:"askedAt"`
	GroupKey        string    `json:"groupKey"`
	AskedTurn       int       `json:"askedTurn,omitempty"`
	AnswerDelivered bool      `json:"answerDelivered,omitempty"`
}
type State struct {
	Version   int                 `json:"version"`
	Next      map[string]int      `json:"next"`
	Questions map[string]Question `json:"questions"`
}
type Store struct {
	Root string
	mu   sync.Mutex
	db   *sql.DB
}

func key(room, id string) string { return room + "/" + id }
func Validate(q Question) error {
	if q.Room == "" || q.Asker == "" {
		return fmt.Errorf("a question needs a room and an asker")
	}
	if !q.AskerKind.valid() {
		return fmt.Errorf("a question needs an asker kind")
	}
	if strings.TrimSpace(q.Meanwhile) == "" {
		return fmt.Errorf("--meanwhile is required: say what useful safe work you will do while you wait")
	}
	if strings.TrimSpace(q.Text) == "" || !utf8.ValidString(q.Text) || utf8.RuneCountInString(q.Text) > 2000 {
		return fmt.Errorf("question text must have 1 to 2000 characters")
	}
	if !utf8.ValidString(q.Meanwhile) || utf8.RuneCountInString(q.Meanwhile) > 2000 {
		return fmt.Errorf("meanwhile text must have at most 2000 characters")
	}
	if len(q.Options) > 6 {
		return fmt.Errorf("a question allows at most 6 options")
	}
	seen := map[string]bool{}
	for _, o := range q.Options {
		if strings.TrimSpace(o) == "" || !utf8.ValidString(o) || utf8.RuneCountInString(o) > 60 || seen[o] {
			return fmt.Errorf("options must be distinct and have 1 to 60 characters")
		}
		seen[o] = true
	}
	return nil
}
func GroupKey(text string, options []string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " ")) + "|" + strings.Join(options, "|")
}

// Ask reserves the question's slot under the open-question limits and its ID,
// posts it, then records the post. A rejected post removes the reservation,
// so it stores no question, unless the question was answered or withdrawn
// meanwhile. IDs are never reused. Posting runs outside the store transaction: it
// takes the asker's session lock, and file locks must come before a dyna.db
// transaction, never inside one.
func (s *Store) Ask(q Question, post func(Question) (string, error)) (Question, error) {
	q.Text, q.Meanwhile = strings.TrimSpace(q.Text), strings.TrimSpace(q.Meanwhile)
	if err := Validate(q); err != nil {
		return Question{}, err
	}
	sessionsRoot := filepath.Join(filepath.Dir(s.Root), "sessions")
	// The record checks below run inside this store's transaction, which
	// cannot create the record table itself.
	if err := sessionlife.Prepare(sessionsRoot); err != nil {
		return Question{}, err
	}
	// Wake the sessions first, each under its own session lock. The
	// lifecycle check repeats inside the store transaction, so a session
	// that closes meanwhile still fails.
	if err := sessionlife.Activate(sessionsRoot, q.Asker); err != nil {
		return Question{}, fmt.Errorf("cannot ask a question: %w", err)
	}
	toErr := sessionlife.Activate(sessionsRoot, q.To)
	err := s.updateTx(func(conn *sql.Conn, st *State) error {
		return s.reserve(conn, sessionsRoot, &q, toErr, st)
	})
	if err != nil || post == nil {
		return q, err
	}
	id, err := post(q)
	k := key(q.Room, q.ID)
	if err != nil {
		// Remove the reservation only while nobody has used it: the owner can
		// answer it during posting, and an answer is never dropped. Its ID is
		// never given out again, because answer delivery is keyed by it.
		if undo := s.update(func(st *State) error {
			if cur, ok := st.Questions[k]; ok && cur.State == "open" && cur.Answer == nil && cur.Withdrawn == nil {
				delete(st.Questions, k)
			}
			return nil
		}); undo != nil {
			err = errors.Join(err, undo)
		}
		return q, err
	}
	q.Post = id
	return q, s.update(func(st *State) error {
		if cur, ok := st.Questions[k]; ok {
			cur.Post = id
			st.Questions[k] = cur
		}
		return nil
	})
}

// reserve checks the asker and the limits, then stores the question under a
// new ID, before it is posted.
func (s *Store) reserve(conn *sql.Conn, sessionsRoot string, q *Question, toErr error, st *State) error {
	if err := sessionlife.CheckTx(conn, sessionsRoot, q.Asker); err != nil {
		return fmt.Errorf("cannot ask a question: %w", err)
	}
	if toErr != nil || sessionlife.CheckTx(conn, sessionsRoot, q.To) != nil {
		q.To = "owner"
	}
	asker, total := 0, 0
	for _, old := range st.Questions {
		if old.Room == q.Room && old.State == "open" {
			total++
			if old.Asker == q.Asker {
				asker++
			}
		}
	}
	if asker >= 10 || total >= 200 {
		return fmt.Errorf("open question limit reached; use dyna ask withdraw <id> --why \"no longer needed\"")
	}
	q.ID = fmt.Sprintf("q%d", st.Next[q.Room]+1)
	q.State = "open"
	q.AskedAt = time.Now()
	q.GroupKey = GroupKey(q.Text, q.Options)
	if q.To == "" {
		q.To = "owner"
	}
	st.Next[q.Room]++
	st.Questions[key(q.Room, q.ID)] = *q
	return nil
}

// List returns one room history, or all open questions when room is empty.
func (s *Store) List(room string) ([]Question, error) {
	if room == "" {
		return s.listQuestions("state='open'", false)
	}
	return s.listQuestions("room=?", false, room)
}

func (s *Store) Answer(room, id string, a Answer) ([]Question, error) {
	out := []Question{}
	err := s.update(func(st *State) error {
		q, ok := st.Questions[key(room, id)]
		if !ok {
			return fmt.Errorf("no question %s in this room", id)
		}
		if q.State != "open" {
			return fmt.Errorf("question %s is %s", id, q.State)
		}
		a.Text = strings.TrimSpace(a.Text)
		if a.Choice == "" && a.Text == "" {
			return fmt.Errorf("an answer needs text or --choice")
		}
		if !utf8.ValidString(a.Text) || utf8.RuneCountInString(a.Text) > 2000 {
			return fmt.Errorf("answer text must have at most 2000 characters")
		}
		if a.Choice != "" {
			found := false
			for _, o := range q.Options {
				found = found || o == a.Choice
			}
			if !found {
				return fmt.Errorf("choice must match one of the question's options")
			}
		}
		a.At = time.Now()
		for k, other := range st.Questions {
			if other.Room == room && other.State == "open" && other.GroupKey == q.GroupKey && other.To == q.To {
				other.State = "answered"
				other.Answer = &a
				st.Questions[k] = other
				out = append(out, other)
			}
		}
		return nil
	})
	return out, err
}
func (s *Store) MarkDelivered(room, id string) error {
	return s.update(func(st *State) error {
		k := key(room, id)
		q := st.Questions[k]
		q.AnswerDelivered = true
		st.Questions[k] = q
		return nil
	})
}
func (s *Store) Withdraw(room, id, by string, human bool, reason string) (Question, error) {
	var q Question
	err := s.update(func(st *State) error {
		var ok bool
		q, ok = st.Questions[key(room, id)]
		if !ok {
			return fmt.Errorf("no question %s in this room", id)
		}
		if q.State != "open" {
			return fmt.Errorf("question %s is %s", id, q.State)
		}
		q.State = "withdrawn"
		q.Withdrawn = &Withdraw{At: time.Now(), By: by, Human: human, Reason: reason}
		st.Questions[key(room, id)] = q
		return nil
	})
	return q, err
}
func (s *Store) CloseSession(room, asker string) ([]Question, error) {
	out := []Question{}
	err := s.update(func(st *State) error {
		for k, q := range st.Questions {
			if q.Room == room && q.Asker == asker && q.State == "open" {
				q.State = "withdrawn"
				q.Withdrawn = &Withdraw{At: time.Now(), By: asker, Reason: "session closed"}
				st.Questions[k] = q
				out = append(out, q)
			}
		}
		return nil
	})
	return out, err
}

// Reassign moves the open questions asked by from to the session to.
func (s *Store) Reassign(room, from, to string) ([]Question, error) {
	out := []Question{}
	err := s.update(func(st *State) error {
		for k, q := range st.Questions {
			if q.Room == room && q.Asker == from && q.State == "open" {
				q.Asker = to
				st.Questions[k] = q
				out = append(out, q)
			}
		}
		return nil
	})
	return out, err
}

func PostID(room, id, event string) string {
	h := sha256.Sum256([]byte(room + "/" + id + "/" + event))
	return fmt.Sprintf("msg_%x", h[:8])
}

// ListOpen returns a room's open questions, newest first. A read error must
// keep the session when a caller checks whether it can clear dormant work.
func (s *Store) ListOpen(room string) ([]Question, error) {
	return s.listQuestions("room=? AND state='open'", true, room)
}

// OpenAskedBy returns the asker's open questions, newest first.
func (s *Store) OpenAskedBy(room, asker string) ([]Question, error) {
	open, err := s.ListOpen(room)
	out := []Question{}
	for _, q := range open {
		if q.Asker == asker {
			out = append(out, q)
		}
	}
	return out, err
}
