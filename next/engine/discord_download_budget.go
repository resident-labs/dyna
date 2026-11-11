package engine

import (
	"github.com/resident-labs/dyna/contract"
	"time"
)

const discordHourlyBytes int64 = 200 << 20
const discordHourlyLimit = "not downloaded: this room's hourly Discord download limit is reached"

type discordDownloadCharge struct {
	at    time.Time
	bytes int64
}

// Reservations count in-flight downloads too. The caller corrects its charge to
// bytes received, or zero on failure, before storing the file.
func (s *Service) reserveDiscordDownload(room contract.RoomID, size int64) *discordDownloadCharge {
	s.discordDownloadMu.Lock()
	defer s.discordDownloadMu.Unlock()
	if s.discordDownloads == nil {
		s.discordDownloads = make(map[contract.RoomID][]*discordDownloadCharge)
	}
	now := s.now()
	var active []*discordDownloadCharge
	var used int64
	for _, charge := range s.discordDownloads[room] {
		if charge.at.After(now.Add(-time.Hour)) {
			active = append(active, charge)
			used += charge.bytes
		}
	}
	s.discordDownloads[room] = active
	if size > discordHourlyBytes-used {
		return nil
	}
	charge := &discordDownloadCharge{at: now, bytes: max(size, 0)}
	s.discordDownloads[room] = append(active, charge)
	return charge
}
func (s *Service) correctDiscordDownload(room contract.RoomID, charge *discordDownloadCharge, size int64) bool {
	s.discordDownloadMu.Lock()
	defer s.discordDownloadMu.Unlock()
	var used int64
	for _, c := range s.discordDownloads[room] {
		if c != charge && c.at.After(s.now().Add(-time.Hour)) {
			used += c.bytes
		}
	}
	if size > discordHourlyBytes-used {
		charge.bytes = 0
		return false
	}
	charge.bytes = size
	return true
}
