package fakediscord

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// writeFailure makes one numbered write fail, as Discord or the network might.
type writeFailure struct {
	at         int // the write to fail, counted from FailWrite; zero means none
	status     int
	applyFirst bool
}

// FailWrite makes the nth write request from now (1 is the next) answer with
// status. With applyFirst the change still happens, so its outcome is unknown
// to the caller.
func (s *Server) FailWrite(n, status int, applyFirst bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = 0
	s.failure = writeFailure{at: n, status: status, applyFirst: applyFirst}
}

func (s *Server) injectedFailure(method string) (status int, applyFirst bool) {
	if method == "GET" || method == "HEAD" {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.writes != s.failure.at {
		return 0, false
	}
	return s.failure.status, s.failure.applyFirst
}

// newID returns an ID that no configured object uses.
func (s *Server) newID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newIDLocked()
}
func (s *Server) newIDLocked() string {
	s.nextID++
	return strconv.Itoa(5000 + s.nextID)
}

// Configuration returns a copy of the server's current state.
func (s *Server) Configuration() Configuration {
	s.mu.Lock()
	defer s.mu.Unlock()
	var copy Configuration
	data, _ := json.Marshal(s.configuration)
	_ = json.Unmarshal(data, &copy)
	return copy
}

// configurationWrite applies a guild, role, channel or AutoMod write to the
// configuration and answers as Discord does. Other writes are not handled.
func (s *Server) configurationWrite(w http.ResponseWriter, r *http.Request, body []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.configuration
	if c == nil || r.Method == "GET" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v10/"), "/")
	var result any
	status := 404
	switch {
	case len(parts) >= 2 && parts[0] == "guilds" && parts[1] == c.Guild.ID:
		result, status = s.writeGuild(c, r.Method, parts[2:], body)
	case len(parts) == 2 && parts[0] == "channels" && c.channel(parts[1]) != nil:
		result, status = s.writeChannel(c, r.Method, parts[1], body)
	default:
		return false
	}
	w.WriteHeader(status)
	if result != nil {
		_ = json.NewEncoder(w).Encode(result)
	}
	return true
}

// writeGuild handles the routes below /guilds/G; parts are the segments after G.
func (s *Server) writeGuild(c *Configuration, method string, parts []string, body []byte) (any, int) {
	switch {
	case len(parts) == 0:
		if method != "PATCH" {
			return nil, 404
		}
		var patch struct {
			Features []string `json:"features"`
			Rules    string   `json:"rules_channel_id"`
			Updates  string   `json:"public_updates_channel_id"`
		}
		_ = json.Unmarshal(body, &patch)
		if slices.Contains(patch.Features, "COMMUNITY") && (c.channel(patch.Rules) == nil || c.channel(patch.Updates) == nil) {
			return map[string]string{"message": "Community needs rules and updates channels"}, 400
		}
		mergeJSON(&c.Guild, body)
		return c.Guild, 200
	case parts[0] == "roles" && method == "POST":
		role := Role{Position: 1}
		mergeJSON(&role, body)
		role.ID = s.newIDLocked()
		for i := range c.Roles {
			if c.Roles[i].Position >= 1 {
				c.Roles[i].Position++
			}
		}
		c.Roles = append(c.Roles, role)
		return role, 200
	case parts[0] == "roles" && method == "PATCH" && len(parts) == 1:
		return setPositions(body, func(id string) *int {
			if role := c.role(id); role != nil {
				return &role.Position
			}
			return nil
		})
	case parts[0] == "roles" && len(parts) == 2 && c.role(parts[1]) != nil:
		if method == "DELETE" {
			c.Roles = slices.DeleteFunc(c.Roles, func(role Role) bool { return role.ID == parts[1] })
			return nil, 204
		}
		mergeJSON(c.role(parts[1]), body)
		return c.role(parts[1]), 200
	case parts[0] == "channels" && method == "POST":
		channel := Channel{}
		mergeJSON(&channel, body)
		if (channel.Type == 5 || channel.Type == 15) && !slices.Contains(c.Guild.Features, "COMMUNITY") {
			return map[string]string{"message": "This channel type needs Community"}, 400
		}
		channel.ID = s.newIDLocked()
		for _, sibling := range c.Channels {
			if sibling.Parent == channel.Parent && sibling.Position >= channel.Position {
				channel.Position = sibling.Position + 1
			}
		}
		s.giveTagIDs(&channel)
		c.Channels = append(c.Channels, channel)
		return channel, 200
	case parts[0] == "channels" && method == "PATCH":
		return setPositions(body, func(id string) *int {
			if channel := c.channel(id); channel != nil {
				return &channel.Position
			}
			return nil
		})
	case parts[0] == "auto-moderation" && method == "POST":
		rule := AutoMod{}
		mergeJSON(&rule, body)
		rule.ID = s.newIDLocked()
		c.AutoMod = append(c.AutoMod, rule)
		return rule, 200
	case parts[0] == "auto-moderation" && len(parts) == 3 && c.rule(parts[2]) != nil:
		if method == "DELETE" {
			c.AutoMod = slices.DeleteFunc(c.AutoMod, func(rule AutoMod) bool { return rule.ID == parts[2] })
			return nil, 204
		}
		mergeJSON(c.rule(parts[2]), body)
		return c.rule(parts[2]), 200
	}
	return nil, 404
}

func (s *Server) writeChannel(c *Configuration, method, id string, body []byte) (any, int) {
	if method == "DELETE" {
		// Discord leaves a deleted category's children in place, uncategorized.
		for i := range c.Channels {
			if c.Channels[i].Parent == id {
				c.Channels[i].Parent = ""
			}
		}
		c.Channels = slices.DeleteFunc(c.Channels, func(channel Channel) bool { return channel.ID == id })
		return nil, 200
	}
	channel := c.channel(id)
	mergeJSON(channel, body)
	s.giveTagIDs(channel)
	return channel, 200
}

func setPositions(body []byte, position func(id string) *int) (any, int) {
	var changes []struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
	}
	_ = json.Unmarshal(body, &changes)
	for _, change := range changes {
		if position(change.ID) == nil {
			return nil, 404
		}
		*position(change.ID) = change.Position
	}
	return nil, 204
}

func (c *Configuration) role(id string) *Role {
	for i := range c.Roles {
		if c.Roles[i].ID == id {
			return &c.Roles[i]
		}
	}
	return nil
}
func (c *Configuration) channel(id string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].ID == id {
			return &c.Channels[i]
		}
	}
	return nil
}
func (c *Configuration) rule(id string) *AutoMod {
	for i := range c.AutoMod {
		if c.AutoMod[i].ID == id {
			return &c.AutoMod[i]
		}
	}
	return nil
}
func (s *Server) giveTagIDs(channel *Channel) {
	for i := range channel.Tags {
		if channel.Tags[i].ID == "" {
			channel.Tags[i].ID = s.newIDLocked()
		}
	}
}

// mergeJSON overlays the fields present in patch onto target; a null clears a field.
func mergeJSON[T any](target *T, patch []byte) {
	var merged, changes map[string]json.RawMessage
	current, _ := json.Marshal(target)
	_ = json.Unmarshal(current, &merged)
	_ = json.Unmarshal(patch, &changes)
	for key, value := range changes {
		merged[key] = value
	}
	data, _ := json.Marshal(merged)
	var result T
	_ = json.Unmarshal(data, &result)
	*target = result
}
