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

// Kind says what a stored question asks for. A row with no kind is a plain
// question.
type Kind string

const (
	KindQuestion Kind = "question"
	// KindTerminal asks the owner to run a command in a terminal.
	KindTerminal Kind = "terminal"
)

// TerminalVerdict is how the owner ended a terminal request.
type TerminalVerdict string

const (
	VerdictDone     TerminalVerdict = "done"
	VerdictFailed   TerminalVerdict = "failed"
	VerdictDeclined TerminalVerdict = "declined"
)

func (v TerminalVerdict) valid() bool {
	return v == VerdictDone || v == VerdictFailed || v == VerdictDeclined
}

// TerminalStage is where a terminal request stands. A request climbs the
// session chain, each holder approving or denying, and the owner sees it only
// at waiting-owner. The owner's verdict and a denial end it.
type TerminalStage string

const (
	StageWaitingSession TerminalStage = "waiting-session"
	StageWaitingOwner   TerminalStage = "waiting-owner"
	StageDenied         TerminalStage = "denied"
	StageDone           TerminalStage = "done"
	StageFailed         TerminalStage = "failed"
	StageDeclined       TerminalStage = "declined"
	StageWithdrawn      TerminalStage = "withdrawn"
)

// Limits of a terminal request.
const (
	MaxTerminalReason = 500
	MaxTerminalArgv   = 4 << 10
)

// TerminalRequest is what a session asks the owner to run in a terminal, and
// what the owner reports back. SharedLines is set only when the owner chose to
// share the end of the screen. A denial keeps its reason and the denier in the
// question's Answer.
type TerminalRequest struct {
	// Argv is empty when the owner gets a plain login shell.
	Argv   []string      `json:"argv"`
	Cwd    string        `json:"cwd"`
	Reason string        `json:"reason"`
	Stage  TerminalStage `json:"stage"`
	// Holder is the session that decides next, while the stage is
	// waiting-session.
	Holder      string `json:"holder,omitempty"`
	TermID      string `json:"termId,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	SharedLines string `json:"sharedLines,omitempty"`
}

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
	Kind            Kind      `json:"kind,omitempty"`
	// Terminal holds a terminal request's command and result. Replace the
	// pointer on change; the store compares a copy against it.
	Terminal *TerminalRequest `json:"terminal,omitempty"`
}

// IsTerminal reports a terminal request.
func (q Question) IsTerminal() bool { return q.Kind == KindTerminal }

// TerminalStage is the stage of a terminal request, which a withdrawal ends.
func (q Question) TerminalStage() TerminalStage {
	switch {
	case !q.IsTerminal() || q.Terminal == nil:
		return ""
	case q.State == "withdrawn":
		return StageWithdrawn
	}
	return q.Terminal.Stage
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
	if q.IsTerminal() {
		return validateTerminal(q)
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
func validateTerminal(q Question) error {
	t := q.Terminal
	if q.AskerKind != AskerSession || t == nil {
		return fmt.Errorf("only a work session files a terminal request")
	}
	if strings.TrimSpace(q.Text) == "" || !utf8.ValidString(q.Text) || utf8.RuneCountInString(q.Text) > MaxTerminalReason {
		return fmt.Errorf("--reason must have 1 to %d characters", MaxTerminalReason)
	}
	if t.Cwd == "" {
		return fmt.Errorf("a terminal request needs a folder")
	}
	if t.Stage != StageWaitingOwner && (t.Stage != StageWaitingSession || t.Holder == "") {
		return fmt.Errorf("a new terminal request waits for a session or the owner")
	}
	return CheckArgv(t.Argv)
}

// CheckArgv limits a terminal request's command to valid text of bounded size.
func CheckArgv(argv []string) error {
	size := 0
	for _, arg := range argv {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("the command must be valid text")
		}
		size += len(arg) + 1
	}
	if size > MaxTerminalArgv {
		return fmt.Errorf("the command is longer than %d bytes", MaxTerminalArgv)
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
		return fmt.Errorf("open question limit reached; use dyna request withdraw <id> --why \"no longer needed\"")
	}
	q.ID = fmt.Sprintf("q%d", st.Next[q.Room]+1)
	q.State = "open"
	q.AskedAt = time.Now()
	q.GroupKey = GroupKey(q.Text, q.Options)
	if q.IsTerminal() {
		// A terminal request never joins another question's answer.
		q.GroupKey = "terminal:" + q.ID
	}
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
		if q.IsTerminal() {
			return fmt.Errorf("%s is a terminal request; the owner reports it from the terminal", id)
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

// TerminalReport is the owner's result for a terminal request.
type TerminalReport struct {
	Verdict     TerminalVerdict
	ExitCode    *int
	Note        string
	SharedLines string
	Human       bool
}

// editTerminal changes one open terminal request at the given stage. It stores
// a new copy of the request, so the transaction sees the change.
func (s *Store) editTerminal(room, id string, stage TerminalStage, edit func(*Question, *TerminalRequest) error) (Question, error) {
	var out Question
	err := s.update(func(st *State) error {
		q, ok := st.Questions[key(room, id)]
		if !ok || !q.IsTerminal() || q.Terminal == nil {
			return fmt.Errorf("no terminal request %s in this room", id)
		}
		if q.State != "open" || q.Terminal.Stage != stage {
			return fmt.Errorf("request %s is %s, not %s", id, q.TerminalStage(), stage)
		}
		t := *q.Terminal
		if err := edit(&q, &t); err != nil {
			return err
		}
		q.Terminal = &t
		st.Questions[key(room, id)] = q
		out = q
		return nil
	})
	return out, err
}

// byHolder is editTerminal for a request that the session holds.
func (s *Store) byHolder(room, id, holder string, edit func(*Question, *TerminalRequest) error) (Question, error) {
	return s.editTerminal(room, id, StageWaitingSession, func(q *Question, t *TerminalRequest) error {
		if t.Holder != holder {
			return fmt.Errorf("you do not hold request %s", id)
		}
		return edit(q, t)
	})
}

// Approve passes a held request to the next holder, or to the owner when next
// is empty.
func (s *Store) ApproveTerminal(room, id, holder, next string) (Question, error) {
	return s.byHolder(room, id, holder, func(_ *Question, t *TerminalRequest) error {
		t.Holder, t.Stage = next, StageWaitingOwner
		if next != "" {
			t.Stage = StageWaitingSession
		}
		return nil
	})
}

// DenyTerminal ends a held request. The reason is the answer the filer reads.
func (s *Store) DenyTerminal(room, id, holder, reason string) (Question, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 2000 {
		return Question{}, fmt.Errorf("--reason must have 1 to 2000 characters")
	}
	return s.byHolder(room, id, holder, func(q *Question, t *TerminalRequest) error {
		t.Holder, t.Stage = "", StageDenied
		q.State, q.Answer = "answered", &Answer{At: time.Now(), By: holder, Text: reason}
		return nil
	})
}

// ReassignHolder moves the requests that a closing session holds to the next
// holder, or to the owner when to is empty. It returns the moved requests.
func (s *Store) ReassignHolder(room, from, to string) ([]Question, error) {
	var moved []Question
	err := s.update(func(st *State) error {
		for k, q := range st.Questions {
			if q.Room != room || q.State != "open" || q.Terminal == nil || q.Terminal.Stage != StageWaitingSession || q.Terminal.Holder != from {
				continue
			}
			t := *q.Terminal
			t.Holder, t.Stage = to, StageWaitingOwner
			if to != "" {
				t.Stage = StageWaitingSession
			}
			q.Terminal = &t
			st.Questions[k] = q
			moved = append(moved, q)
		}
		return nil
	})
	return moved, err
}

// SetTerminal records the terminal the owner started for a request and the
// command the owner ran, which may differ from the one the session filed. It
// refuses when another call set the terminal since prev, so two runs of one
// request never leave an orphan.
func (s *Store) SetTerminal(room, id string, argv []string, prev, termID string) (Question, error) {
	return s.editTerminal(room, id, StageWaitingOwner, func(_ *Question, t *TerminalRequest) error {
		if t.TermID != prev {
			return fmt.Errorf("request %s already has a terminal", id)
		}
		t.Argv, t.TermID = argv, termID
		return nil
	})
}

// ReportTerminal closes a request that waits for the owner as answered. The
// owner's note is the answer text.
func (s *Store) ReportTerminal(room, id string, r TerminalReport) (Question, error) {
	note := strings.TrimSpace(r.Note)
	if !r.Verdict.valid() {
		return Question{}, fmt.Errorf("verdict must be done, failed or declined")
	}
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > 2000 {
		return Question{}, fmt.Errorf("note must have at most 2000 characters")
	}
	return s.editTerminal(room, id, StageWaitingOwner, func(q *Question, t *TerminalRequest) error {
		t.Stage, t.ExitCode, t.SharedLines = TerminalStage(r.Verdict), r.ExitCode, r.SharedLines
		q.State = "answered"
		q.Answer = &Answer{At: time.Now(), By: "owner", Human: r.Human, Text: note}
		return nil
	})
}
