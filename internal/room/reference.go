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
