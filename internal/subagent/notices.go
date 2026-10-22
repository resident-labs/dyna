package subagent

import (
	"fmt"
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
// The key holds one notice per profile; a newer failure replaces its text.
func postCooldownNotice(s notices.Store, profileName, failure string, at time.Time) error {
	wait := harness.UnavailableUntil(failure, at).Sub(at)
	text := fmt.Sprintf("%s: provider limit reached, resets in %dh", profileName, int(wait.Round(time.Hour).Hours()))
	if wait < 90*time.Minute {
		text = fmt.Sprintf("%s: provider limit reached, resets in %dm", profileName, int(wait.Round(time.Minute).Minutes()))
	}
	_, err := s.Post(notices.Notice{Key: "provider:" + profileName, Kind: "provider", Severity: "warn", Text: text, At: at})
	return err
}

// postAccountSwitch tells root, quietly, that a turn moved to another account.
func (m *Manager) postAccountSwitch(meta Meta, sw harness.AccountSwitch) {
	text := fmt.Sprintf("%s hit its usage limit; %s's turn moved to %s.", sw.From, meta.Address(), sw.To)
	if _, err := m.rooms().Post(meta.RoomID, room.PostInput{Wake: false, From: "dyna", Kind: "account", To: []string{meta.Room + ":" + RoleRoot}, Text: text}); err != nil {
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
	if _, err := m.rooms().Post(meta.RoomID, room.PostInput{Wake: false, ID: transferNoticeID(token), From: "dyna", Kind: "account", To: []string{meta.Address()}, Text: t.Notice}); err != nil {
		log.Printf("fallback notice: %v", err)
		return
	}
	_ = m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error { t.NoticePosted = true; return nil })
}
