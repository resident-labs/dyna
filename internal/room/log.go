package room

import (
	"fmt"
	"slices"
	"strings"
)

// LogOptions selects a focused room history. Nil Tail means 30, or unlimited
// with All. Explicit kinds replace the default agent-post filter.
type LogOptions struct {
	All   bool
	Tail  *int
	Since string
	Kinds []string
}

// Log returns matching posts in append order and the count cut by the tail.
// Since must identify a stored post, even if that post does not match filters.
func (s *Store) Log(rec Record, opts LogOptions) ([]Message, int, error) {
	tail := 30
	if opts.All {
		tail = 0
	}
	if opts.Tail != nil {
		tail = *opts.Tail
	}
	if tail < 0 {
		return nil, 0, fmt.Errorf("--tail must be at least 0")
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil {
		return nil, 0, err
	}
	if opts.Since != "" {
		if msgs, err = After(msgs, opts.Since, rec.Name); err != nil {
			return nil, 0, err
		}
	}
	out := make([]Message, 0)
	for _, msg := range msgs {
		if len(opts.Kinds) > 0 {
			if !slices.Contains(opts.Kinds, msg.Kind) || (!opts.All && !ForOwner(msg, rec.Name)) {
				continue
			}
		} else if !opts.All && !DefaultView(msg, rec.Name) {
			continue
		}
		out = append(out, msg)
	}
	hidden := 0
	if tail > 0 && len(out) > tail {
		hidden = len(out) - tail
		out = out[hidden:]
	}
	return out, hidden, nil
}

// After returns the posts after the post with this ID. Room log and room
// watch both take --since <post-id> through it.
func After(msgs []Message, id, roomName string) ([]Message, error) {
	index := slices.IndexFunc(msgs, func(m Message) bool { return m.ID == id })
	if index < 0 {
		return nil, fmt.Errorf("post %q not found in room %s", id, roomName)
	}
	return msgs[index+1:], nil
}

// DefaultView selects room log's owner feed: agent messages, notices and
// session turn reports, including failed turns. Room watch also carries root
// context. Shell and workflow results stay hidden here; --all shows them.
func DefaultView(msg Message, roomName string) bool {
	return ForOwner(msg, roomName) && agentPost(msg, roomName)
}

// agentPost keeps ordinary messages, including workflow worker messages.
// System completions share result kinds, so those also require a session sender.
func agentPost(msg Message, roomName string) bool {
	// A registered system event follows its spec: a quiet event is not agent
	// input, and a non-quiet one is.
	if spec, ok := eventSpecFor(msg.Kind, msg.Notice.Kind); ok {
		return !spec.Quiet
	}
	switch msg.Kind {
	case "", "message", "notice", "seat":
		return true
	case "completed", "failed", "stopped":
		return strings.HasPrefix(msg.From, roomName+":")
	default:
		return false
	}
}

// ForOwner includes posts to the owner from others, and every post the human
// owner typed with a mention, whoever it addresses. Root shares the owner's
// authority, so it must see the human's @team and direct posts too. Owner
// posts the orchestrator makes itself never echo back, and human records
// without a mention (spawn and task records) stay hidden.
func ForOwner(msg Message, roomName string) bool {
	if msg.From == roomName {
		return msg.Human && (msg.Kind == "" || msg.Kind == "message") && len(msg.Via) > 0
	}
	return msg.Addressed(roomName)
}
