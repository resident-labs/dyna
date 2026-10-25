package roomsvc

import (
	"hash/fnv"
	"sort"
	"time"

	"dyna-agent/internal/subagent"
)

// emojiPool holds the avatar glyphs: animals, plants and objects that read
// well at 20-80px. It leaves out faces, flags and anything that looks like
// a status (colored circles, checks, crosses, warnings), because gold, green
// and red already mean live, done and error. No glyph needs a variation
// selector, so each one renders as one emoji everywhere.
var emojiPool = []string{
	"🦊", "🐙", "🦉", "🐢", "🦋", "🐝", "🦔", "🐧", "🦦", "🐳", "🦜", "🐌", "🦀", "🐞", "🦩", "🐬",
	"🦭", "🐨", "🐼", "🦥", "🦘", "🦙", "🐸", "🦎", "🐡", "🦈", "🐊", "🦚", "🦢", "🐇", "🦫", "🐋",
	"🌵", "🌻", "🍄", "🌿", "🍀", "🌸", "🌲", "🌴", "🌾", "🌼", "🪴", "🪸", "🍋", "🍉", "🍒", "🥝",
	"🍑", "🥥", "🫐", "🍩", "🧁", "🍪", "🎈", "🪁", "🧩", "🎲", "🔭", "🧭", "🪐", "🌙", "🔮", "🪶",
}

// avatarBook assigns each session in a room one emoji. Sessions are taken in
// creation order, closed ones included, and each takes the first free glyph
// from its own hashed start. Closing a session never moves anyone, and a new
// session only adds itself, so an avatar stays the same once shown.
type avatarBook struct {
	byID   map[string]string
	byAddr map[string][]addrAvatar // address -> sessions that held it, oldest first
	// addrByID and openByAddr name sessions of every room, not only this one, so
	// a reply finds the session that wrote a post after a rename or a move.
	addrByID   map[string]string // session -> the address it answers at now
	openByAddr map[string]string // address -> the session that answers to it now
}

type addrAvatar struct {
	id      string
	created time.Time
	emoji   string
}

func newAvatarBook(sessions []subagent.Info, roomID string) avatarBook {
	ordered := append([]subagent.Info{}, sessions...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Created.Equal(ordered[j].Created) {
			return ordered[i].Created.Before(ordered[j].Created)
		}
		return ordered[i].ID < ordered[j].ID
	})
	b := avatarBook{byID: map[string]string{}, byAddr: map[string][]addrAvatar{}, addrByID: map[string]string{}, openByAddr: map[string]string{}}
	for _, s := range ordered {
		b.addrByID[s.ID] = s.Address()
		// An open session answers to its address, as a mention resolves. In
		// creation order the newest holder wins, and a closed session holds
		// nothing: a live session can take a closed one's name.
		if s.Status != subagent.StatusClosed {
			b.openByAddr[s.Address()] = s.ID
		}
	}
	var in []subagent.Info
	for _, s := range ordered {
		if s.RoomID == roomID {
			in = append(in, s)
		}
	}
	used := map[int]bool{}
	for _, s := range in {
		h := fnv.New32a()
		_, _ = h.Write([]byte(s.ID))
		start := int(h.Sum32() % uint32(len(emojiPool)))
		pick := start
		if len(used) < len(emojiPool) {
			for used[pick] {
				pick = (pick + 1) % len(emojiPool)
			}
		}
		used[pick] = true
		b.byID[s.ID] = emojiPool[pick]
		b.byAddr[s.Address()] = append(b.byAddr[s.Address()], addrAvatar{id: s.ID, created: s.Created, emoji: emojiPool[pick]})
	}
	return b
}

// forPost is the avatar of the session that held addr when the post was
// written, so a reused name keeps old avatars on old posts.
func (b avatarBook) forPost(addr string, ts int64) string {
	held := b.byAddr[addr]
	out := ""
	for _, h := range held {
		if h.created.UnixMilli() <= ts || out == "" {
			out = h.emoji
		}
	}
	return out
}

// sessionsFor lists the IDs of every session that has held addr in this
// room, oldest first. A name can be reused after a close.
func (b avatarBook) sessionsFor(addr string) []string {
	var out []string
	for _, h := range b.byAddr[addr] {
		out = append(out, h.id)
	}
	return out
}

// address is the address session id answers at now, wherever the session's
// room is. ok is false when the snapshot does not hold the session, or when
// another open session answers to that address now: a reply names the session
// a mention reaches, never a stranger that took the name.
func (b avatarBook) address(id string) (string, bool) {
	cur, ok := b.addrByID[id]
	if !ok || b.openByAddr[cur] != id {
		return "", false
	}
	return cur, true
}

// avatars returns the book for one room, from the cached session snapshot.
func (s *Service) avatars(roomID string) avatarBook {
	all, rev := s.snapshot()
	return s.avatarBook(roomID, all, rev)
}
