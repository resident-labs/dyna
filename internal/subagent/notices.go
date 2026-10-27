package subagent

import (
	"log"
	"path/filepath"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/room"
)

func (m *Manager) notices() notices.Store {
	return notices.Store{Dir: filepath.Dir(m.store().Root)}
}

// postCooldownNotice tells the owner that a profile entered provider cooldown.
// The key holds one notice per profile; a newer failure replaces its facts.
func postCooldownNotice(s notices.Store, profileName, failure string, at time.Time) error {
	// A zero time means the provider named none; a guess is never shown.
	resets := harness.UsageResetAt(failure, at)
	_, err := s.Post(notices.Notice{
		Key:      "provider:" + profileName,
		Kind:     notices.KindProvider,
		Severity: notices.SeverityWarn,
		Provider: &notices.ProviderFacts{Profile: profileName, ResetsAt: resets},
		At:       at,
	})
	return err
}

// postAccountSwitch tells root, quietly, that a turn moved to another account.
func (m *Manager) postAccountSwitch(meta Meta, sw harness.AccountSwitch) {
	// The same account pair can recur, so each switch is its own note.
	event := room.SessionEvent{Type: room.SessionAccount, Session: meta.Address(), From: sw.From, To: sw.To, Reason: room.AccountUsageLimit}
	if _, err := m.rooms().PostEventInput(room.PostEventInput{RoomID: meta.RoomID, Event: event, FreshID: true, To: []string{meta.Room + ":" + RoleRoot}}); err != nil {
		log.Printf("account switch note: %v", err)
	}
}

// postFallbackNotice tells the session's own DM that a usage limit moved it to
// its fallback profile, and says that nothing moves it back automatically.
// Root is not alerted: the session continues without help.
func (m *Manager) postFallbackNotice(id, token string) {
	t, err := m.TransferStatus(id)
	if err != nil || t == nil || t.Notice == "" || t.NoticePosted {
		return
	}
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return
	}
	event := room.SessionEvent{
		Type:    room.SessionAccount,
		Session: meta.Address(),
		From:    t.PreviousProfile,
		To:      t.Profile,
		Reason:  room.AccountFallback,
		Detail:  t.Notice,
	}
	if _, err := m.rooms().PostEventInput(room.PostEventInput{RoomID: meta.RoomID, Event: event, ID: transferNoticeID(token), To: []string{meta.Address()}}); err != nil {
		log.Printf("fallback notice: %v", err)
		return
	}
	_ = m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error { t.NoticePosted = true; return nil })
}
