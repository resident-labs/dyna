package subagent

import (
	"fmt"
	"sort"
	"strings"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
)

// maxGroupShown bounds the members listed per selector in a turn frame.
const maxGroupShown = 8

func statusRank(status string) int {
	switch status {
	case StatusRunning:
		return 0
	case StatusQueued:
		return 1
	default:
		return 2
	}
}

// selectorGroup lists what a group selector resolves to now: every open
// member, running first, then by recent activity.
func selectorGroup(members []Info, room string) string {
	if len(members) == 0 {
		return "none"
	}
	sort.SliceStable(members, func(i, j int) bool {
		ri, rj := statusRank(members[i].Status), statusRank(members[j].Status)
		if ri != rj {
			return ri < rj
		}
		if !members[i].LastActivity.Equal(members[j].LastActivity) {
			return members[i].LastActivity.After(members[j].LastActivity)
		}
		return members[i].Address() < members[j].Address()
	})
	shown := members
	if len(shown) > maxGroupShown {
		shown = shown[:maxGroupShown]
	}
	parts := make([]string, len(shown))
	for i, info := range shown {
		parts[i] = fmt.Sprintf("%s (%s)", info.Address(), info.Status)
	}
	out := strings.Join(parts, ", ")
	if extra := len(members) - len(shown); extra > 0 {
		out += fmt.Sprintf(" +%d more; see dyna room members %s", extra, room)
	}
	return out
}

// SelectorContext renders the room frame of a turn prompt: the room and the
// session's own address, what each selector resolves to now, and where the
// room's history is.
func (s *Store) SelectorContext(meta Meta) string {
	parent := "@root"
	if meta.ParentSession != "" {
		if info, err := s.Inspect(meta.ParentSession); err == nil {
			parent = info.Address()
			if info.Status == StatusClosed {
				parent += " (closed; mention @root instead)"
			}
		}
	}
	var siblings, children []Info
	if sessions, err := s.ListSnapshot(false); err == nil {
		for _, info := range sessions {
			if info.ID == meta.ID || info.RoomID != meta.RoomID {
				continue
			}
			if !info.Service && info.ParentSession == meta.ParentSession {
				siblings = append(siblings, info)
			}
			if info.ParentSession == meta.ID {
				children = append(children, info)
			}
		}
	}
	return fmt.Sprintf("Hierarchy: depth %d (top level = 1). Stored metadata controls parent selectors and child creation.\n", meta.Depth) + "Room: " + meta.Room + "   You: " + meta.Address() + "\n" +
		"Selectors: @parent = " + parent +
		" · @siblings = " + selectorGroup(siblings, meta.Room) +
		" · @children = " + selectorGroup(children, meta.Room) + "\n" +
		"History: dyna room log " + meta.Room + " (read once if your task depends on earlier decisions; never poll)"
}

// ReplyAddress is the address a recipient should mention to answer a post
// from from. A workflow script (run:<id>) cannot receive posts, so replies go
// to the run's parent session, or to the run's room owner. A shell cannot
// receive either; it has no reply address. A session sender is found by the
// ID bound to its post, so a renamed sender still gets its current address.
// A registered engine event reports state and asks for no answer, so it has
// no reply address either: the engine sender is not a recipient, and its name
// can also be a room's name.
func (s *Store) ReplyAddress(msg room.Message) string {
	from := msg.From
	ref := from
	if id := msg.SessionIDs[from]; id != "" {
		ref = id
	}
	// A typed event names its own subject: a turn end answers its session, a
	// run result its parent, and a shell result has no reader to answer.
	if _, ok := msg.Shell(); ok {
		return ""
	}
	if ev, ok := msg.TurnResult(); ok {
		if info, err := s.replyTarget(ev.SessionID); err == nil && info.Closed.IsZero() {
			return info.Address()
		}
		return ev.Session
	}
	if ev, ok := msg.RunResult(); ok {
		if ev.Parent != "" {
			if parent, err := s.replyTarget(ev.Parent); err == nil && parent.Closed.IsZero() {
				return parent.Address()
			}
		}
		if meta, err := runstore.ReadMeta(ev.RunID); err == nil {
			return meta.Room
		}
		return ""
	}
	// Every other registered engine event only reports state. A reply would
	// name the engine sender, which can also be a room's name, so the answer
	// would land in that room's root seat instead of where the agent works.
	if msg.EngineEvent() {
		return ""
	}
	// TODO: legacy/shim remove later — a shell or run result stored before the
	// typed events names its job in the sender. Remove when retained room
	// history ages out.
	switch {
	case msg.Kind == string(room.FamilyCheckin) || strings.HasPrefix(from, "shell:"):
		return ""
	case strings.HasPrefix(from, "run:") && !strings.Contains(from, "/agent:"):
		meta, err := runstore.ReadMeta(strings.TrimPrefix(from, "run:"))
		if err != nil {
			return ""
		}
		if meta.ParentSession != "" {
			if parent, err := s.replyTarget(meta.ParentSession); err == nil && parent.Closed.IsZero() {
				return parent.Address()
			}
		}
		return meta.Room
	}
	if info, err := s.replyTarget(ref); err == nil && info.Closed.IsZero() {
		return info.Address()
	}
	return from
}

// replyTarget runs while the recipient gate can be held. It must never use
// Info/List and trigger orphan repair or a notification callback under it. It
// answers from one indexed lookup, then follows consumption redirects without
// repair, so a reply arrives at the session that receives the posts.
func (s *Store) replyTarget(ref string) (Meta, error) {
	var meta Meta
	if runstore.IsSessionDirID(ref) {
		found, err := s.ReadMeta(ref)
		if err != nil {
			return found, err
		}
		return s.consumedTarget(found)
	}
	room, name, ok := strings.Cut(ref, ":")
	if !ok || room == "" {
		return meta, ErrNotFound
	}
	ids, err := sessionlife.Lookup(s.root(), room, name)
	if err != nil {
		return meta, err
	}
	// An address is reused after a session closes, so the oldest session that
	// still holds it answers, as the ID scan did.
	sort.Strings(ids)
	for _, id := range ids {
		candidate, err := s.ReadMeta(id)
		if err == nil && candidate.Address() == ref {
			return s.consumedTarget(candidate)
		}
	}
	return meta, ErrNotFound
}
