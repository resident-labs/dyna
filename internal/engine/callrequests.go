package engine

import (
	"context"
	"log"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// ringPush is how long a call rings in the app before a push goes out.
const ringPush = 5 * time.Second

// deliverCallRequests also runs at startup, so pending delivery survives a
// restart. It returns when the next ringing call's push is due, or zero.
func deliverCallRequests(ctx context.Context, rooms *roomsvc.Service, push pushNotifier, now time.Time) (time.Time, error) {
	st := rooms.Notify.CallRequestStore()
	rows, err := st.List("")
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	for _, row := range rows {
		if row.State == "ringing" {
			if push == nil {
				continue
			}
			if due := row.AskedAt.Add(ringPush); now.Before(due) {
				if next.IsZero() || due.Before(next) {
					next = due
				}
				continue
			}
			claimed, send, err := st.ClaimPush(row.Room, row.ID)
			if err != nil {
				return next, err
			}
			if send {
				push.Notify(ctx, websvc.Event{Kind: "call", Room: row.Room, Call: row.ID, Text: claimed.Reason})
			}
			continue
		}
		if row.CloseDelivered {
			continue
		}
		rec, err := rooms.Notify.Rooms.Resolve(row.Room)
		if err != nil {
			return next, err
		}
		state := room.CallWithdrawn
		if row.State == "joined" {
			state = room.CallJoined
		} else if row.State == "declined" {
			state = room.CallDeclined
		}
		// The answer is a typed call event, so the card names the call and the
		// agent text carries no raw ID in the middle of a sentence. A stable ID
		// makes a crash between posting and recording delivery safe.
		//
		// Root asked for the call and stops waiting on the answer, so the post
		// needs root: a registered event wakes the root seat only when it does
		// (room.rootWakes), and the voice post this replaced always did.
		_, err = rooms.Notify.Post(ctx, notify.PostInput{
			ID:        questions.PostID(row.Room, row.ID, "call-closed"),
			NeedsRoot: true,
			Event:     room.CallEvent{Type: state, Call: row.ID},
			Sender:    notify.Sender{Kind: notify.SenderOwner, Room: rec.Name},
			Mentions:  []string{"@root"},
		})
		if err != nil {
			return next, err
		}
		if err := st.MarkCloseDelivered(row.Room, row.ID); err != nil {
			return next, err
		}
	}
	return next, nil
}

// callRetry is how soon a failed delivery pass runs again.
const callRetry = 5 * time.Second

// runCallRequests delivers at start, then on each call-requests event (wake)
// and when the next ringing call's push is due, with no clock tick.
func runCallRequests(ctx context.Context, rooms *roomsvc.Service, push pushNotifier, wake chan struct{}, t *timers) {
	again := func() { wakeWorker(wake) }
	for {
		next, err := deliverCallRequests(ctx, rooms, push, t.now())
		if err != nil {
			log.Printf("call requests: %v", err)
			t.at(timerCallRetry, t.now().Add(callRetry), again)
		}
		if !next.IsZero() {
			t.at(timerCallRing, next, again)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}
