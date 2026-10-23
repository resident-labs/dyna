package room

import (
	"fmt"
	"math"
	"net/url"
	"strings"
)

// Reference is a snapshot of a pull request or issue selected for a post.
type Reference struct {
	Kind   string `json:"kind"`
	Number any    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
}

// issueReferences resolves old message IDs without changing the append-only log.
// Canonical D-N snapshots need no database lookup.
type issueReferences struct {
	store *Store
	rooms map[string]map[string]int
}

func (r *issueReferences) canonical(roomID string, refs []Reference) error {
	for i, ref := range refs {
		if ref.Kind != "room_issue" {
			continue
		}
		var number int
		switch n := ref.Number.(type) {
		case int:
			number = n
		case float64:
			number = int(n)
		case string:
			if parsed, ok := issueNumber(n); ok {
				number = parsed
			} else {
				if r.rooms == nil {
					r.rooms = map[string]map[string]int{}
				}
				numbers, loaded := r.rooms[roomID]
				if !loaded {
					db, err := r.store.issuesDB()
					if err != nil {
						return err
					}
					rows, err := db.Query(`SELECT message_id,number FROM room_issue_numbers WHERE room_id=?`, roomID)
					if err != nil {
						return err
					}
					numbers = map[string]int{}
					for rows.Next() {
						var id string
						var value int
						if err := rows.Scan(&id, &value); err != nil {
							rows.Close()
							return err
						}
						numbers[id] = value
					}
					err = rows.Err()
					rows.Close()
					if err != nil {
						return err
					}
					r.rooms[roomID] = numbers
				}
				number = numbers[n]
			}
		}
		if number > 0 {
			refs[i].Number = fmt.Sprintf("D-%d", number)
		}
	}
	return nil
}

// ValidateReferences bounds untrusted composer metadata before storage.
func ValidateReferences(refs []Reference) error {
	if len(refs) > 20 {
		return fmt.Errorf("a post can carry at most 20 references")
	}
	for _, r := range refs {
		if r.Kind != "pr" && r.Kind != "issue" && r.Kind != "room_issue" {
			return fmt.Errorf("unknown reference kind %q", r.Kind)
		}
		switch n := r.Number.(type) {
		case int:
			if n <= 0 {
				return fmt.Errorf("reference number must be positive")
			}
		case float64:
			if n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) || math.Trunc(n) != n {
				return fmt.Errorf("reference number must be a positive integer")
			}
		case string:
			if r.Kind != "room_issue" || strings.TrimSpace(n) == "" || len(n) > 128 || strings.ContainsAny(n, "\r\n") {
				return fmt.Errorf("only room issues accept a nonempty string ID")
			}
		default:
			return fmt.Errorf("reference number must be a positive integer or room issue ID")
		}
		if strings.TrimSpace(r.Title) == "" || len(r.Title) > 1024 || strings.ContainsAny(r.Title, "\r\n") {
			return fmt.Errorf("reference title must be one nonempty line of at most 1024 bytes")
		}
		if len(r.State) > 64 || strings.ContainsAny(r.State, "\r\n") {
			return fmt.Errorf("reference state must be one line of at most 64 bytes")
		}
		if len(r.URL) > 2048 || strings.ContainsAny(r.URL, "\r\n") {
			return fmt.Errorf("reference URL is too long or contains a newline")
		}
		if r.URL != "" {
			u, err := url.Parse(r.URL)
			if err == nil && r.Kind == "room_issue" && strings.HasPrefix(r.URL, "#") {
				q, queryErr := url.ParseQuery(u.Fragment)
				if queryErr == nil && len(q) == 2 && len(q["room"]) == 1 && q.Get("room") != "" && len(q["view"]) == 1 && q.Get("view") == "issues" {
					continue
				}
			}
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return fmt.Errorf("reference URL must be HTTP(S) or a room issue fragment")
			}
		}
	}
	return nil
}

func referenceText(body string, refs []Reference) string {
	for _, r := range refs {
		label := "Issue"
		if r.Kind == "pr" {
			label = "PR"
		} else if r.Kind == "room_issue" {
			label = "Room issue"
		}
		line := fmt.Sprintf("%s #%v: %s", label, r.Number, r.Title)
		if r.Kind == "room_issue" {
			if number, ok := issueNumber(fmt.Sprint(r.Number)); ok {
				line = fmt.Sprintf("%s D-%d: %s", label, number, r.Title)
			}
		}
		var details []string
		if r.State != "" {
			details = append(details, r.State)
		}
		if r.URL != "" {
			details = append(details, r.URL)
		}
		if len(details) > 0 {
			line += " (" + strings.Join(details, ", ") + ")"
		}
		if body != "" {
			body += "\n"
		}
		body += line
	}
	return body
}
