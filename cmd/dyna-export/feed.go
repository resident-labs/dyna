package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/room"
)

type issueFeed struct {
	filed room.IssueEvent
	notes []room.Message
}

type roomFeed struct {
	issues          map[string]issueFeed
	lastPosts       map[string]string
	partial         bool
	legacyIssueRefs map[string]bool
	legacyIssues    int
}

// readFeed opens the append-only feed without using live store APIs.
func readFeed(feeds, roomID string) (roomFeed, error) {
	if !room.ValidID(roomID) {
		return roomFeed{}, fmt.Errorf("invalid room ID")
	}
	root, err := os.OpenRoot(feeds)
	if err != nil {
		return roomFeed{}, err
	}
	defer root.Close()
	f, err := root.Open(filepath.Join("rooms", roomID, "messages.jsonl"))
	if err != nil {
		return roomFeed{}, fmt.Errorf("captured room feed: %w", err)
	}
	defer f.Close()
	out := roomFeed{issues: make(map[string]issueFeed), lastPosts: make(map[string]string), legacyIssueRefs: make(map[string]bool)}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			out.partial = true
			return len(data), nil, nil
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		var msg room.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return roomFeed{}, fmt.Errorf("decode captured room feed: %w", err)
		}
		if msg.Text != "" {
			text := []rune(msg.Text)
			if len(text) > 2000 {
				text = text[:2000]
			}
			out.lastPosts[msg.From] = string(text)
		}
		event, ok := msg.Issue()
		if !ok {
			if msg.Kind == string(room.FamilyIssue) {
				if msg.Payload.IsZero() {
					out.legacyIssues++
					out.legacyIssueRefs[msg.ID] = true
					if msg.ReplyTo != "" {
						out.legacyIssueRefs[msg.ReplyTo] = true
					}
					continue
				}
				raw, err := msg.Payload.MarshalJSON()
				if err != nil {
					return roomFeed{}, fmt.Errorf("issue feed payload: %w", err)
				}
				var event room.IssueEvent
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&event); err != nil {
					return roomFeed{}, fmt.Errorf("decode issue feed payload: %w", err)
				}
				return roomFeed{}, fmt.Errorf("issue feed payload type is missing")
			}
			continue
		}
		id := event.IssueID
		if event.Type == room.IssueFiled {
			id = msg.ID
		}
		item := out.issues[id]
		if event.Type == room.IssueFiled {
			item.filed = event
		} else if event.Type == room.IssueNoted {
			item.notes = append(item.notes, msg)
		}
		out.issues[id] = item
	}
	if err := scanner.Err(); err != nil {
		return roomFeed{}, err
	}
	return out, nil
}
