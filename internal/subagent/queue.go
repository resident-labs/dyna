package subagent

import (
	"context"
	"fmt"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

// QueueState is how far one queued post got toward its target session.
type QueueState string

const (
	QueueWaiting    QueueState = "waiting"
	QueueDelivering QueueState = "delivering"
	QueueDelivered  QueueState = "delivered"
	QueueForwarded  QueueState = "forwarded"
	QueueCancelled  QueueState = "cancelled"
	QueueDismissed  QueueState = "dismissed"
)

// queueFacts are the stored delivery and steering facts that project one queue
// state. Terminal delivery facts take precedence over steering receipts.
type queueFacts struct {
	dispatchedTurn int
	claimedTurn    int
	deliveredTurn  int
	cancelled      bool
	dismissed      bool
}

func queueStateFromFacts(facts queueFacts) QueueState {
	switch {
	case facts.dismissed:
		return QueueDismissed
	case facts.cancelled:
		return QueueCancelled
	case facts.deliveredTurn > 0:
		return QueueDelivered
	case facts.dispatchedTurn > 0 || facts.claimedTurn > 0:
		return QueueDelivering
	default:
		return QueueWaiting
	}
}

type QueueOrigin struct {
	Session         string     `json:"session"`
	Address         string     `json:"address"`
	State           QueueState `json:"state"`
	TargetDelivered bool       `json:"targetDelivered,omitempty"`
}
type QueueItem struct {
	Human       bool          `json:"human,omitempty"`
	Origins     []QueueOrigin `json:"origins,omitempty"`
	ID          string        `json:"id"`
	Text        string        `json:"text"`
	From        string        `json:"from"`
	TS          int64         `json:"ts"`
	Turn        int           `json:"turn"`
	State       QueueState    `json:"state"`
	Cancellable bool          `json:"cancellable"`
	ForwardedTo string        `json:"forwardedTo,omitempty"`
}

func (s *Store) SessionQueue(rooms *room.Store, ref string) ([]QueueItem, error) {
	info, err := s.Resolve(ref)
	if err != nil {
		return nil, err
	}
	out := []QueueItem{}
	dismissed := map[string]bool{}
	err = s.withGateLock(info.ID, func(dir string) error {
		claims, err := sessionClaims(rooms, info.Meta)
		if err != nil {
			return err
		}
		dismissed = claims.dismissed
		delivered := map[string]int{}
		dispatched := map[string]int{}
		steers, err := s.steering(info.ID, info.Turns)
		if err != nil {
			return err
		}
		for _, st := range steers {
			dispatched[st.ID] = st.Turn
			if st.Delivered {
				delivered[st.ID] = st.Turn
			}
		}
		msgs, err := rooms.Messages(info.RoomID)
		if err != nil {
			return err
		}
		input, err := forSession(rooms, info.Meta, msgs)
		if err != nil {
			return err
		}
		for _, msg := range input {
			if msg.TS < info.Created.UnixMilli() && dispatched[msg.ID] == 0 && claims.claimedTurn[msg.ID] == 0 {
				continue
			}
			facts := queueFacts{
				dispatchedTurn: dispatched[msg.ID],
				claimedTurn:    claims.claimedTurn[msg.ID],
				deliveredTurn:  delivered[msg.ID],
				cancelled:      claims.cancelled[msg.ID],
				dismissed:      claims.dismissed[msg.ID],
			}
			item := QueueItem{ID: msg.ID, Text: msg.Text, From: msg.From, Human: msg.Human, TS: msg.TS, State: queueStateFromFacts(facts), Cancellable: msg.From == info.Room}
			if facts.dispatchedTurn > 0 {
				item.Turn, item.Cancellable = facts.dispatchedTurn, false
			}
			if facts.claimedTurn > 0 {
				item.Turn, item.Cancellable = facts.claimedTurn, false
			}
			if facts.deliveredTurn > 0 {
				item.Turn, item.Cancellable = facts.deliveredTurn, false
			}
			if facts.cancelled || facts.dismissed {
				item.Cancellable = false
			}
			out = append(out, item)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	forwarded, err := s.forwardedPosts(info.ID)
	if err != nil {
		return nil, err
	}
	markForwarded := func() {
		for i := range out {
			if id := forwarded[out[i].ID]; id != "" {
				out[i].State, out[i].Cancellable, out[i].ForwardedTo = QueueForwarded, false, id
			}
		}
	}
	if len(info.InheritedInboxes) == 0 {
		markForwarded()
		return out, err
	}
	index := map[string]int{}
	received, err := s.deliveredIDs(info.ID, info.Turns)
	if err != nil {
		return nil, err
	}
	for id, done := range info.InheritedReceipts {
		received[id] = received[id] || done
	}
	for i, item := range out {
		index[item.ID] = i
		out[i].Origins = []QueueOrigin{{Session: info.ID, Address: info.Address(), State: item.State, TargetDelivered: received[item.ID]}}
	}
	for _, in := range info.InheritedInboxes {
		items, e := s.SessionQueue(rooms, in.Session)
		if e != nil {
			return nil, e
		}
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		inherited, err := inputRows(rooms, info.RoomID, in.Session, in.Address, ids)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if _, e := rooms.Message(info.RoomID, item.ID); e != nil {
				return nil, e
			}
			if _, theirs := inherited[item.ID]; !theirs {
				continue
			}
			at, exists := index[item.ID]
			if !exists {
				at = len(out)
				index[item.ID] = at
				copy := item
				copy.Origins = nil
				copy.Cancellable = false
				out = append(out, copy)
			}
			if received[item.ID] {
				out[at].State = QueueDelivered
				out[at].Cancellable = false
			} else if item.State == QueueWaiting || item.State == QueueDelivering {
				out[at].State = QueueWaiting
			}
			origin := QueueOrigin{Session: in.Session, Address: in.Address, State: item.State, TargetDelivered: received[item.ID]}
			found := false
			for _, old := range out[at].Origins {
				if old.Session == origin.Session {
					found = true
				}
			}
			if !found {
				out[at].Origins = append(out[at].Origins, origin)
			}
		}
	}
	for i := range out {
		if dismissed[out[i].ID] {
			out[i].State, out[i].Cancellable = QueueDismissed, false
		}
	}
	markForwarded()
	return out, nil
}

// DeletePendingSteer cancels only the owner's posts before the runner claims
// them. This never removes history or changes delivery to other recipients.
func (s *Store) DeletePendingSteer(rooms *room.Store, ref, id string) error {
	info, err := s.Resolve(ref)
	if err != nil {
		return err
	}
	return s.deletePendingSteer(rooms, info.Meta, id)
}

// deletePendingSteer accepts an already verified snapshot, including callers
// holding session.lock. It must not resolve/repair that session recursively.
func (s *Store) deletePendingSteer(rooms *room.Store, info Meta, id string) error {
	return s.withGateLock(info.ID, func(dir string) error {
		claims, err := sessionClaims(rooms, info)
		if err != nil {
			return err
		}
		msg, err := rooms.Message(info.RoomID, id)
		if err != nil {
			return err
		}
		_, delivered, err := inputRow(rooms, info.RoomID, info.ID, info.Address(), id)
		if err != nil {
			return err
		}
		if msg.From != info.Room || !delivered || msg.TS < info.Created.UnixMilli() {
			return fmt.Errorf("only an owner's waiting post to this session can be deleted")
		}
		if claims.cancelled[id] {
			return nil
		}
		if claims.claimedTurn[id] > 0 {
			return fmt.Errorf("message was already claimed for delivery")
		}
		for n := 1; n <= info.Turns; n++ {
			t, err := s.ReadTurn(info.ID, n)
			if err != nil {
				return err
			}
			for _, st := range t.Steering {
				if st.ID == id {
					return fmt.Errorf("message was already dispatched")
				}
			}
		}
		cancelled, err := s.cancelPost(context.Background(), rooms, info, id)
		if err != nil {
			return err
		}
		if !cancelled {
			return fmt.Errorf("message was already claimed for delivery")
		}
		return nil
	})
}

// queueSig is the newest write of session id's delivery rows, or "" when it
// has none. Those rows hold the claim, cancellation and dismissal state that
// the queue row held before them, so their newest write is the queue's
// version.
func (s *Store) queueSig(id string) string {
	roomID, address, err := sessionlife.DeliveryAddress(s.root(), id)
	if err != nil {
		return ""
	}
	return s.deliverySignature(id, roomID, address)
}

// QueueSignature changes when claim, cancellation or delivery state changes.
func (s *Store) QueueSignature(id string) string { return s.queueSig(id) }

// QueueSignatureOf is QueueSignature for a session whose record the caller
// just read, such as a roster scan holding every record already: it skips the
// delivery-address query, which is half the scan's signature reads. The
// record's address is the same string the queue readers key rows by
// (SessionQueue), so a stale record can only miss rows a fresh one finds for
// the one scan before the record write wakes the next one.
func (s *Store) QueueSignatureOf(meta Meta) string {
	return s.deliverySignature(meta.ID, meta.RoomID, meta.Address())
}

// TurnSignature changes when turn number turns of session id is written,
// which changes the transcript or queue display: it is the turn row's
// revision. turns comes from the session's record, which the caller has
// already read; 0 has no turn.
func (s *Store) TurnSignature(id string, turns int) string {
	if turns < 1 {
		return ""
	}
	version, ok := s.turnVersion(id, turns)
	if !ok {
		return ""
	}
	return "/" + version
}

// deliveredPostTurn uses the delivery row's claim and the acknowledged
// steering record. Claiming alone does not prove delivery to the harness, and a
// row that says delivered proves it without the turn record.
func (s *Store) deliveredPostTurn(id, messageID string) (Turn, bool, error) {
	var turn Turn
	delivered := false
	err := s.withGateLock(id, func(dir string) error {
		meta, err := s.ReadMeta(id)
		if err != nil {
			return err
		}
		row, ok, err := inputRow(s.deliveryRooms(), meta.RoomID, meta.ID, meta.Address(), messageID)
		if err != nil {
			return err
		}
		n := row.ClaimedTurn
		if !ok || n == 0 {
			return nil
		}
		turn, err = s.ReadTurn(id, n)
		if err != nil {
			return err
		}
		delivered = turn.DeliveredPost(messageID) || row.State == room.DeliveryDelivered
		return nil
	})
	return turn, delivered, err
}
