package subagent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

// WrapUpProvider returns short reminders before a session goes idle.
// Providers must only read state. They do not start work or deliver posts.
type WrapUpProvider func(session Info) []string

func (m *Manager) questionStore() *questions.Store {
	return &questions.Store{Root: filepath.Join(filepath.Dir(m.store().Root), "questions")}
}
func (m *Manager) questionWrapUp(info Info) []string {
	qs, err := m.questionStore().List(info.RoomID)
	if err != nil {
		return nil
	}
	var items []string
	for _, q := range qs {
		if q.State != "open" || q.Asker != info.ID || q.AskedTurn >= info.Turns {
			continue
		}
		text := []rune(q.Text)
		if len(text) > 60 {
			text = append(text[:60], '…')
		}
		age := time.Since(q.AskedAt).Round(time.Minute)
		if age < time.Minute {
			age = time.Minute
		}
		items = append(items, fmt.Sprintf("%s %q (%s)", q.ID, string(text), age))
	}
	if len(items) == 0 {
		return nil
	}
	return []string{"Open questions you asked: " + strings.Join(items, ", ") + ".\n  Withdraw any that no longer matter with `dyna ask withdraw <id> --why \"…\"`."}
}
func (m *Manager) wrapUp(ctx context.Context, id string, n int) {
	store := m.store()
	turn, err := store.ReadTurn(id, n)
	if err != nil || turn.Status != TurnOK || turn.WrapUp || turn.Prompt == "" {
		return
	}
	info, err := store.Info(id)
	if err != nil || info.Status != StatusIdle || !info.Resumable || info.HarnessSession == "" || info.Turns != n || info.Transfer.blocksTurns() || info.Consolidating != "" {
		return
	}
	// A real turn between reminders permits another wrap-up, even if the
	// provider lines are unchanged. The same real turn is never wrapped twice.
	if m.RoomPaused(info.RoomID) {
		return
	}
	if info.WrapUpAfter >= n {
		return
	}
	providers := append([]WrapUpProvider{m.questionWrapUp}, m.WrapUpProviders...)
	var lines []string
	for _, p := range providers {
		lines = append(lines, p(info)...)
	}
	markChecked := func() {
		_, _ = store.updateMetaContext(ctx, id, func(meta *Meta) {
			if meta.WrapUpAfter < n {
				meta.WrapUpAfter = n
			}
		})
	}
	if len(lines) == 0 {
		// A later quiet note must not retry this completion with new provider state.
		markChecked()
		return
	}
	prompt := "[DYNA WRAP-UP] Before you go idle:\n- " + strings.Join(lines, "\n- ") + "\nReply in one line. Do not start new work."
	if result, err := m.Wake(ctx, id, WakeOptions{continuation: prompt, wrapUp: true}); err == nil && !result.Steered && !result.Paused {
		markChecked()
	}
}
func (m *Manager) withdrawClosedQuestions(info Info) error {
	// Avoid creating a questions store for sessions that never asked anything.
	qs, err := m.questionStore().List(info.RoomID)
	if err != nil {
		return err
	}
	found := false
	for _, q := range qs {
		found = found || q.Asker == info.ID && q.State == "open"
	}
	if !found {
		return nil
	}
	withdrawn, err := m.questionStore().CloseSession(info.RoomID, info.ID)
	if err != nil {
		return err
	}
	for _, q := range withdrawn {
		// TODO(legacy): Some older questions have state but no room post.
		if q.Post == "" {
			continue
		}
		post, readErr := m.rooms().Message(info.RoomID, q.Post)
		if readErr != nil {
			return readErr
		}
		if _, err = m.rooms().Post(info.RoomID, room.PostInput{ID: questions.PostID(q.Room, q.ID, "withdrawn"), From: info.Address(), Kind: "message", To: post.To, ReplyTo: q.Post, Text: "[WITHDRAWN " + q.ID + "] session closed"}); err != nil {
			return err
		}
	}
	return nil
}
