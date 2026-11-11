package discord

import (
	"encoding/json"
	"sync"
)

type Channel struct {
	ID     string `json:"id"`
	Guild  string `json:"guild_id"`
	Parent string `json:"parent_id"`
	Name   string `json:"name"`
	Type   int    `json:"type"`
}
type Role struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Server struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Channels []Channel `json:"channels"`
	Threads  []Channel `json:"threads"`
	Roles    []Role    `json:"roles"`
}

// Directory is gateway-owned public metadata, shared by routing and tool scope checks.
type Directory struct {
	mu       sync.RWMutex
	guilds   map[string]Server
	channels map[string]Channel
}

func (d *Directory) Apply(kind string, data json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.guilds == nil {
		d.guilds = map[string]Server{}
		d.channels = map[string]Channel{}
	}
	switch kind {
	case "GUILD_CREATE", "GUILD_UPDATE":
		var g Server
		if json.Unmarshal(data, &g) != nil || g.ID == "" {
			return
		}
		if kind == "GUILD_UPDATE" {
			old := d.guilds[g.ID]
			old.Name = g.Name
			d.guilds[g.ID] = old
			return
		}
		d.guilds[g.ID] = g
		for _, c := range append(g.Channels, g.Threads...) {
			c.Guild = g.ID
			d.channels[c.ID] = c
		}
	case "GUILD_DELETE":
		var g Server
		if json.Unmarshal(data, &g) != nil {
			return
		}
		delete(d.guilds, g.ID)
		for id, c := range d.channels {
			if c.Guild == g.ID {
				delete(d.channels, id)
			}
		}
	case "CHANNEL_CREATE", "CHANNEL_UPDATE", "THREAD_CREATE", "THREAD_UPDATE":
		var c Channel
		if json.Unmarshal(data, &c) == nil && c.ID != "" {
			d.channels[c.ID] = c
		}
	case "CHANNEL_DELETE", "THREAD_DELETE":
		var c Channel
		if json.Unmarshal(data, &c) == nil {
			delete(d.channels, c.ID)
		}
	case "GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE":
		var event struct {
			Guild string `json:"guild_id"`
			Role  Role   `json:"role"`
		}
		if json.Unmarshal(data, &event) != nil {
			return
		}
		g := d.guilds[event.Guild]
		found := false
		for i := range g.Roles {
			if g.Roles[i].ID == event.Role.ID {
				g.Roles[i] = event.Role
				found = true
			}
		}
		if !found {
			g.Roles = append(g.Roles, event.Role)
		}
		d.guilds[event.Guild] = g
	}
}
func (d *Directory) Channel(id string) (Channel, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c, ok := d.channels[id]
	return c, ok
}
func (d *Directory) Server(id string) (Server, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	g, ok := d.guilds[id]
	return g, ok
}
func (d *Directory) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.guilds = map[string]Server{}
	d.channels = map[string]Channel{}
}
