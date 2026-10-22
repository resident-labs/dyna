package questions

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Store) PendingRooms() ([]string, error) {
	st, err := s.Read()
	if err != nil {
		return nil, err
	}
	rooms := map[string]bool{}
	for _, q := range st.Questions {
		if q.State == "answered" && !q.AnswerDelivered {
			rooms[q.Room] = true
		}
	}
	out := []string{}
	for room := range rooms {
		out = append(out, room)
	}
	return out, nil
}

// retryAfter is how soon a failed answer delivery is tried again.
const retryAfter = time.Minute

// Tick tries each room with saved answers once. Failed answers stay pending,
// and Tick returns when to retry them, counted from now, or zero when nothing
// failed.
func (s *Store) Tick(ctx context.Context, now time.Time, send func(context.Context, string) error) (time.Time, error) {
	retry := now.Add(retryAfter)
	rooms, err := s.PendingRooms()
	if err != nil {
		return retry, err
	}
	var errs []error
	for _, room := range rooms {
		if err := ctx.Err(); err != nil {
			return time.Time{}, errors.Join(append(errs, err)...)
		}
		if err := send(ctx, room); err != nil {
			errs = append(errs, fmt.Errorf("room %s: %w", room, err))
		}
	}
	if len(errs) > 0 {
		return retry, errors.Join(errs...)
	}
	return time.Time{}, nil
}
