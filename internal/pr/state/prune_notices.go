package state

import (
	"context"
	"time"
)

// DoneNoticeKeep is how long a delivered notice stays. Its ID stops the same
// event from posting twice, so it outlives the delivery, but not for ever.
const DoneNoticeKeep = 14 * 24 * time.Hour

// PruneDoneNotices removes notices delivered more than DoneNoticeKeep before
// now and stamps delivered ones that predate DoneAt. It reports how many it
// removed.
func PruneDoneNotices(ctx context.Context, root string, now time.Time) (int, error) {
	removed := 0
	err := Update(ctx, root, func(st *State) error {
		removed = 0
		for _, r := range st.Records {
			if r == nil {
				continue
			}
			kept := r.Notices[:0:0]
			for _, n := range r.Notices {
				switch {
				case !n.Done:
				case n.DoneAt.IsZero():
					n.DoneAt = now
				case now.Sub(n.DoneAt) > DoneNoticeKeep:
					removed++
					continue
				}
				kept = append(kept, n)
			}
			if len(kept) != len(r.Notices) || len(kept) > 0 {
				r.Notices = kept
			}
		}
		return nil
	})
	return removed, err
}
