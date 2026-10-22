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
}

type addrAvatar struct {
	id      string
	created time.Time
	emoji   string
}

func newAvatarBook(sessions []subagent.Info, roomID string) avatarBook {
	var in []subagent.Info
	for _, s := range sessions {
		if s.RoomID == roomID {
			in = append(in, s)
		}
	}
	sort.SliceStable(in, func(i, j int) bool {
		if !in[i].Created.Equal(in[j].Created) {
			return in[i].Created.Before(in[j].Created)
		}
		return in[i].ID < in[j].ID
	})
	b := avatarBook{byID: map[string]string{}, byAddr: map[string][]addrAvatar{}}
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

// avatars returns the book for one room, from the cached session snapshot.
func (s *Service) avatars(roomID string) avatarBook {
	all, rev := s.snapshot()
	return s.avatarBook(roomID, all, rev)
}
