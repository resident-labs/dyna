// Package links reads session links from the PR watcher's atomic state
// file. It has no subagent dependency, so session close can check PRs without
// importing the watcher and creating an import cycle.
package links

import (
	"sort"

	"dyna-agent/internal/pr/state"
)

type Link struct {
	ChangeID  string   `json:"changeId,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	LookupDir string   `json:"lookupDir,omitempty"`
	Head      string   `json:"head,omitempty"`
	Key       string   `json:"key"`
	Number    int      `json:"number"`
	URL       string   `json:"url"`
	State     string   `json:"state"`
	Watching  bool     `json:"watching"`
	Roles     []string `json:"roles"`
}

func (l Link) Active() bool { return l.Watching && l.State == "open" }

// RoleLink is one link and the session that holds a role in it.
type RoleLink struct {
	Link
	Session string
}

// ForSession reads one consistent snapshot. The writer uses atomic rename;
// readers never create the PR directory or change the state.
func ForSession(root, session, roomID string) ([]Link, error) {
	st, err := state.ReadForSessions(root, roomID, session)
	if err != nil {
		return nil, err
	}
	return FromState(st, session, roomID), nil
}

// FromState projects links from a snapshot whose caller already owns any lock.
func FromState(st state.State, session, roomID string) []Link {
	links := []Link{}
	for _, owned := range RoleLinks(st, roomID) {
		if owned.Session == session {
			links = append(links, owned.Link)
		}
	}
	return links
}

// RoleLinks lists every session with a role in a room's links, so a caller
// that needs the links of many sessions at once reads the snapshot only once.
// FromState filters this list, so both paths share one rule.
func RoleLinks(st state.State, roomID string) []RoleLink {
	links := []RoleLink{}
	for key, r := range st.Records {
		if r == nil {
			continue
		}
		if r.RoomID != roomID {
			continue
		}
		active := r.Watching && r.State == "open"
		roles := map[string][]string{}
		if r.Author != "" {
			roles[r.Author] = append(roles[r.Author], "author")
		}
		if r.Reviewer != "" {
			roles[r.Reviewer] = append(roles[r.Reviewer], "reviewer")
		}
		if active && r.ReviewerOverride != "" && r.ReviewerOverride != r.Reviewer {
			roles[r.ReviewerOverride] = append(roles[r.ReviewerOverride], "reviewer")
		}
		for session, held := range roles {
			links = append(links, RoleLink{Session: session, Link: Link{ChangeID: r.ChangeID, Kind: r.Kind, Dir: r.Dir, LookupDir: r.LookupDir, Head: r.Head, Key: key, Number: r.Number, URL: r.URL, State: r.State, Watching: r.Watching, Roles: held}})
		}
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Key != links[j].Key {
			return links[i].Key < links[j].Key
		}
		return links[i].Session < links[j].Session
	})
	return links
}
