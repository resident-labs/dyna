package websvc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// TicketLife is how long a card URL works. A ticket sits in a URL, so it
// can reach browser history, screenshots and a tunnel's edge logs. It grants
// a read of one card for this long, and it can be reused inside that window
// (the card guard reloads a frame once). The server never logs a query
// string.
const TicketLife = 10 * time.Minute

// Tickets signs card URLs with the web key. The card origin sees no cookie
// (a cloudflared card hostname differs from the app's), so the ticket is
// its only grant.
type Tickets struct {
	key []byte
	now func() time.Time
}

// NewTickets signs with key and the real clock. Server makes its own from
// the store key; the desktop view listener uses a key made at start.
func NewTickets(key []byte) Tickets { return Tickets{key: key, now: time.Now} }

func (t Tickets) mac(roomID, id string, exp int64) string {
	m := hmac.New(sha256.New, t.key)
	// Room and attachment IDs pass room.ValidID and room.ValidAttachmentID,
	// so they never hold the NUL separator.
	m.Write([]byte("dyna-card\x00" + roomID + "\x00" + id + "\x00" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Issue returns the ticket for one card: "<unix expiry>.<mac>".
func (t Tickets) Issue(roomID, id string) (string, time.Time) {
	exp := t.now().Add(TicketLife).Truncate(time.Second)
	return strconv.FormatInt(exp.Unix(), 10) + "." + t.mac(roomID, id, exp.Unix()), exp
}

// Valid checks a ticket for one card in constant time.
func (t Tickets) Valid(roomID, id, ticket string) bool {
	expS, mac, ok := strings.Cut(ticket, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || !t.now().Before(time.Unix(exp, 0)) {
		return false
	}
	return hmac.Equal([]byte(mac), []byte(t.mac(roomID, id, exp)))
}
