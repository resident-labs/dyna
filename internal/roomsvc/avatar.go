package roomsvc

import (
	"dyna-agent/internal/room"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"sync"
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
const senderUnknown room.SenderKind = "unknown"

var unknownSenders sync.Map

type avatarBook struct {
	avatars map[string]Avatar
	rooms   map[string]room.Record
	byID    map[string]string
	byAddr  map[string][]addrAvatar // address -> sessions that held it, oldest first
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
	b := avatarBook{avatars: map[string]Avatar{}, rooms: map[string]room.Record{}, byID: map[string]string{}, byAddr: map[string][]addrAvatar{}, addrByID: map[string]string{}, openByAddr: map[string]string{}}
	for _, s := range ordered {
		b.addrByID[s.ID] = s.Address()
		// An open session answers to its address, as a mention resolves. In
		// creation order the newest holder wins, and a closed session holds
		// nothing: a live session can take a closed one's name.
		if s.Status != subagent.StatusClosed {
			b.openByAddr[s.Address()] = s.ID
		}
	}
	usedByRoom := map[string]map[int]bool{}
	for _, s := range ordered {
		used := usedByRoom[s.RoomID]
		if used == nil {
			used = map[int]bool{}
			usedByRoom[s.RoomID] = used
		}
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
		kind, emoji := room.SenderSession, emojiPool[pick]
		if s.Role == subagent.RoleRoot {
			kind, emoji = room.SenderRoot, "🖥️"
		}
		b.avatars[s.ID] = Avatar{Kind: kind, Emoji: emoji, Room: s.Room}
		b.byAddr[s.Address()] = append(b.byAddr[s.Address()], addrAvatar{id: s.ID, created: s.Created, emoji: emojiPool[pick]})
	}
	return b
}

// resolve returns the sender's home-room identity for a post.
func (b avatarBook) resolve(m room.Message, roomName string) Avatar {
	kind := m.SenderKindFor(roomName)
	home := roomName
	if kind == room.SenderSession || kind == room.SenderRoot {
		id := m.SessionIDs[m.From]
		if id == "" {
			for _, held := range b.byAddr[m.From] {
				if held.created.UnixMilli() <= m.TS || id == "" {
					id = held.id
				}
			}
		}
		if avatar, ok := b.avatars[id]; ok {
			avatar.Color = b.rooms[avatar.Room].Color
			return avatar
		}
		if kind == room.SenderRoot {
			if prefix, _, ok := strings.Cut(m.From, ":"); ok {
				home = prefix
			}
			return Avatar{Kind: kind, Emoji: "🖥️", Room: home, Color: b.rooms[home].Color}
		}
		if _, loaded := unknownSenders.LoadOrStore(m.From, struct{}{}); !loaded {
			log.Printf("unknown avatar sender %q", m.From)
		}
		return Avatar{Kind: senderUnknown, Room: home, Color: b.rooms[home].Color}
	}
	if m.SenderRoomID != "" {
		for _, rec := range b.rooms {
			if rec.ID == m.SenderRoomID {
				home = rec.Name
				break
			}
		}
	}
	avatar := Avatar{Kind: kind, Room: home, Color: b.rooms[home].Color}
	switch kind {
	case room.SenderEngine:
		avatar.Emoji = "⚙️"
	case room.SenderVoice:
		avatar.Emoji = "🎙️"
	}
	return avatar
}

func (b avatarBook) member(id, roomName string) Avatar {
	if id == "" {
		return Avatar{Kind: room.SenderOwner, Room: roomName, Color: b.rooms[roomName].Color}
	}
	if a, ok := b.avatars[id]; ok {
		a.Color = b.rooms[a.Room].Color
		return a
	}
	return Avatar{Kind: senderUnknown, Room: roomName, Color: b.rooms[roomName].Color}
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
