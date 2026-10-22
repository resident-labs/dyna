package subagent

import (
	"fmt"
	"sort"
	"strings"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
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
	parent := "@root (quiet unless --needs-root)"
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
func (s *Store) ReplyAddress(msg room.Message) string {
	from := msg.From
	ref := from
	if id := msg.SessionIDs[from]; id != "" {
		ref = id
	}
	switch {
	case from == room.GoalKeeperSender || strings.HasPrefix(from, "shell:"):
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
// Info/List and trigger orphan repair or a notification callback under it.
func (s *Store) replyTarget(ref string) (Meta, error) {
	var meta Meta
	var err error
	if runstore.IsSessionDirID(ref) {
		meta, err = s.ReadMeta(ref)
	} else if strings.Contains(ref, ":") {
		ids, e := s.sessionIDs()
		if e != nil {
			return meta, e
		}
		found := false
		for _, id := range ids {
			candidate, e := s.ReadMeta(id)
			if e == nil && candidate.Address() == ref {
				meta = candidate
				found = true
				break
			}
		}
		if !found {
			return meta, ErrNotFound
		}
	} else {
		return meta, ErrNotFound
	}
	if err != nil {
		return meta, err
	}
	seen := map[string]bool{}
	for meta.ConsumedBy != "" {
		if seen[meta.ID] {
			return Meta{}, fmt.Errorf("consolidation redirect cycle")
		}
		seen[meta.ID] = true
		next, e := s.ReadMeta(meta.ConsumedBy)
		if e != nil {
			return Meta{}, e
		}
		if next.RoomID != meta.RoomID {
			return Meta{}, fmt.Errorf("cross-room redirect")
		}
		meta = next
	}
	return meta, nil
}
