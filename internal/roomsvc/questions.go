package roomsvc

import (
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/subagent"
	"errors"
	"strings"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

type Question struct {
	questions.Question
	AskerName    string            `json:"askerName"`
	AskerHue     int               `json:"askerHue"`
	AskerEmoji   string            `json:"askerEmoji"`
	ToName       string            `json:"toName,omitempty"`
	WithdrawnBy  string            `json:"withdrawnBy,omitempty"`
	AnswererName string            `json:"answererName,omitempty"`
	WithdrawnHue int               `json:"withdrawnHue"`
	Attachments  []room.Attachment `json:"attachments"`
}

// Questions returns rows grouped by room ID. An empty ref selects open
// questions in active rooms; a room ref selects that room's complete history.
func (s *Service) Questions(ref string) (map[string][]Question, error) {
	roomID := ""
	records := map[string]room.Record{}
	if ref != "" {
		rec, err := s.rooms().Resolve(ref)
		if err != nil {
			return nil, err
		}
		roomID = rec.ID
		records[rec.ID] = rec
	}
	qs, err := s.Notify.QuestionStore().List(roomID)
	if err != nil {
		return nil, err
	}
	out := map[string][]Question{}
	if roomID != "" {
		out[roomID] = []Question{}
	}
	if len(qs) == 0 {
		return out, nil
	}
	if roomID == "" {
		active, err := s.rooms().ListWithActivity(false, s.Notify.ActiveRoomIDs())
		if err != nil {
			return nil, err
		}
		for _, rec := range active {
			records[rec.ID] = rec
		}
	}
	byRoom := map[string][]questions.Question{}
	for _, q := range qs {
		// Archived and deleted rooms stay out of the all-rooms view.
		if _, ok := records[q.Room]; ok {
			byRoom[q.Room] = append(byRoom[q.Room], q)
		}
	}
	if len(byRoom) == 0 {
		return out, nil
	}

	sessions, rev, names := s.questionActors()
	for id, list := range byRoom {
		rec := records[id]
		rows, err := s.questionRows(rec, list, s.avatarBook(id, sessions, rev), names)
		if err != nil {
			return nil, err
		}
		out[id] = rows
	}
	return out, nil
}

func (s *Service) questionActors() ([]subagent.Info, string, map[string]string) {
	sessions, rev := s.snapshot()
	names := map[string]string{}
	for _, info := range sessions {
		names[info.ID] = info.EffectiveName()
	}
	return sessions, rev, names
}

func (s *Service) questionRows(rec room.Record, qs []questions.Question, book avatarBook, names map[string]string) ([]Question, error) {
	name := func(id string, human bool) string {
		if id == "owner" {
			return room.OwnerLabel(human)
		}
		if label, ok := names[id]; ok {
			return label
		}
		return id
	}
	out := []Question{}
	for _, q := range qs {
		addr := rec.Name
		if q.Asker != "owner" {
			addr += ":" + name(q.Asker, q.Human)
		}
		row := Question{Question: q, AskerName: name(q.Asker, q.Human), AskerHue: NameHue(addr), AskerEmoji: book.byID[q.Asker], Attachments: []room.Attachment{}}
		if q.Answer != nil {
			row.AnswererName = name(q.Answer.By, q.Answer.Human)
		}
		if q.To != "owner" {
			row.ToName = name(q.To, false)
		}
		if q.Withdrawn != nil {
			row.WithdrawnBy = name(q.Withdrawn.By, q.Withdrawn.Human)
			withdrawnAddr := rec.Name
			if q.Withdrawn.By != "owner" {
				withdrawnAddr += ":" + row.WithdrawnBy
			}
			row.WithdrawnHue = NameHue(withdrawnAddr)
		}
		if q.Post != "" {
			post, err := s.rooms().Message(rec.ID, q.Post)
			if err != nil && !errors.Is(err, room.ErrPostNotFound) {
				return nil, err
			}
			if err == nil {
				row.Attachments = post.Attachments
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// Visual is one visualization: its newest version first, then every
// version, newest first. ID is the post that created it.
type Visual struct {
	ID         string          `json:"id"`
	Post       Post            `json:"post"`
	Attachment room.Attachment `json:"attachment"`
	Versions   []VisualVersion `json:"versions"`
}

type VisualVersion struct {
	Post       Post            `json:"post"`
	Attachment room.Attachment `json:"attachment"`
}

// Visuals reads every visualization in a room, including posts outside the
// feed's loaded window. The most recently updated comes first.
func (s *Service) Visuals(ref string) ([]Visual, error) {
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return nil, err
	}
	msgs, err := s.rooms().Messages(rec.ID)
	if err != nil {
		return nil, err
	}
	book := s.avatars(rec.ID)
	out := []Visual{}
	at := map[string]int{}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		for _, a := range m.Attachments {
			if a.Render != room.RenderHTML {
				continue
			}
			version := VisualVersion{Post: decorate(m, rec.Name, nil, book), Attachment: a}
			id := a.VisualID(m.ID)
			if j, ok := at[id]; ok {
				out[j].Versions = append(out[j].Versions, version)
				continue
			}
			at[id] = len(out)
			out = append(out, Visual{ID: id, Post: version.Post, Attachment: a, Versions: []VisualVersion{version}})
		}
	}
	return out, nil
}
func (s *Service) scanQuestions(signatures map[string]string, version *string, first bool, emit func(Event)) {
	questionStore := s.Notify.QuestionStore()
	current, err := questionStore.Revision()
	if err != nil || current == *version {
		return
	}
	st, err := questionStore.Read()
	if err != nil {
		return
	}
	byRoom := map[string][]questions.Question{}
	for _, q := range st.Questions {
		byRoom[q.Room] = append(byRoom[q.Room], q)
	}
	rows := []listchange.Item{}
	if len(byRoom) > 0 {
		records, err := s.rooms().List(true)
		if err != nil {
			return
		}
		sessions, rev, names := s.questionActors()
		for _, rec := range records {
			if len(byRoom[rec.ID]) == 0 {
				continue
			}
			items, err := s.questionRows(rec, byRoom[rec.ID], s.avatarBook(rec.ID, sessions, rev), names)
			if err != nil {
				return
			}
			rows = append(rows, listchange.Rows("questions", rec.ID, items, nil, func(q Question) string { return q.ID })...)
		}
	}
	changes, err := listchange.Sync(signatures, rows, first)
	if err != nil {
		return
	}
	*version = current
	woke := map[string]bool{}
	for _, change := range changes {
		emit(Event{Kind: "questions", RoomID: change.RoomID, Change: &change.Change, WorkerWake: !woke[change.RoomID]})
		woke[change.RoomID] = true
	}
}

// legacyQuestionPost restores participant routing on old withdrawal records.
// TODO: legacy/shim remove later — restores old withdrawal routing; remove after retained room logs migrate.
func (s *Service) legacyQuestionPost(rec room.Record, m room.Message) room.Message {
	if m.Kind != "question-withdrawn" {
		return m
	}
	id, _, _ := strings.Cut(m.Text, " ")
	list, err := s.Notify.QuestionStore().List(rec.ID)
	if err != nil {
		return m
	}
	for _, q := range list {
		if q.ID != id || q.Post == "" {
			continue
		}
		original, err := s.rooms().Message(rec.ID, q.Post)
		if err != nil {
			return m
		}
		m.From, m.To, m.Kind = original.From, append([]string{}, original.To...), "message"
		if q.Withdrawn != nil && q.Withdrawn.By != q.Asker {
			if q.Withdrawn.By == "owner" {
				m.From = rec.Name
			} else if info, err := s.sessions().Inspect(q.Withdrawn.By); err == nil {
				m.From = info.Address()
			}
			m.To = append(m.To, original.From)
		}
		to := m.To[:0]
		seen := map[string]bool{m.From: true}
		for _, addr := range m.To {
			if !seen[addr] {
				to = append(to, addr)
				seen[addr] = true
			}
		}
		m.To = to
		m.ReplyTo = q.Post
		m.Text = "[WITHDRAWN " + id + "] " + strings.TrimPrefix(m.Text, id+" withdrawn: ")
		return m
	}
	return m
}
