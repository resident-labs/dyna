package main

import (
	"bufio"
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
	issues    map[string]issueFeed
	lastPosts map[string]string
}

// readFeed consumes only the captured feed. It never follows live store APIs.
func readFeed(data, roomID string) (roomFeed, error) {
	if !room.ValidID(roomID) {
		return roomFeed{}, fmt.Errorf("invalid room ID")
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		return roomFeed{}, err
	}
	defer root.Close()
	f, err := root.Open(filepath.Join("rooms", roomID, "messages.jsonl"))
	if err != nil {
		return roomFeed{}, fmt.Errorf("captured room feed: %w", err)
	}
	defer f.Close()
	out := roomFeed{issues: make(map[string]issueFeed), lastPosts: make(map[string]string)}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 16<<20)
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
