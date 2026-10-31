package watch

import (
	"crypto/sha256"
	"fmt"
	"log"
	"time"

	"dyna-agent/internal/room"
)

// postQuotaWarnings reads only committed observations. Event IDs distinguish
// each account without exposing its login in the card or payload.
func (s *Service) postQuotaWarnings(st State, now time.Time) {
	for _, r := range st.Records {
		if r == nil || !r.Watching || r.RoomID == "" || r.QuotaAccount == "" || changeKind(*r) != "github" {
			continue
		}
		rec, err := s.roomsStore().Resolve(r.RoomID)
		if err != nil {
			log.Printf("GitHub quota warning: resolve room: %v", err)
			continue
		}
		for _, resource := range []string{"core", "graphql"} {
			scope := (RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: resource}).Normalize()
			rate := st.RateLimits[scope.Key()]
			if rate.Limit == nil || *rate.Limit <= 0 || rate.Remaining == nil || *rate.Remaining < 0 || *rate.Remaining > *rate.Limit/5 || !now.Before(rate.Reset) {
				continue
			}
			sum := sha256.Sum256([]byte(r.RoomID + "\x00" + scope.Key() + "\x00" + rate.Reset.UTC().Format(time.RFC3339Nano)))
			ev := room.GitHubQuotaEvent{Host: scope.Host, Resource: resource, Remaining: *rate.Remaining, Limit: *rate.Limit, Reset: rate.Reset.UTC().Format(time.RFC3339)}
			if _, err := s.roomsStore().PostEventInput(room.PostEventInput{RoomID: r.RoomID, Event: ev, To: []string{rec.Name}, ID: fmt.Sprintf("msg_%x", sum[:8])}); err != nil {
				log.Printf("GitHub quota warning: %v", err)
			}
		}
	}
}
