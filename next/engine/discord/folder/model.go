// Package folder describes a room's private, engine-host Discord folder.
package folder

import (
	"encoding/json"
	"strings"
)

type Identity struct {
	ID  string `yaml:"id,omitempty"`
	Was string `yaml:"was,omitempty"`
}
type Server struct {
	Settings   Settings  `yaml:"settings"`
	Categories []string  `yaml:"categories,omitempty"`
	Roles      []Role    `yaml:"roles,omitempty"`
	AutoMod    []AutoMod `yaml:"automod,omitempty"`
}
type Settings struct {
	Verification  string     `yaml:"verification,omitempty"`
	Notifications string     `yaml:"notifications,omitempty"`
	Community     *Community `yaml:"community,omitempty"`
}
type Community struct {
	Rules   string `yaml:"rules"`
	Updates string `yaml:"updates"`
}
type Role struct {
	Identity    `yaml:",inline"`
	Name        string   `yaml:"name"`
	Permissions []string `yaml:"permissions,omitempty"`
	Color       string   `yaml:"color,omitempty"`
	Hoist       bool     `yaml:"hoist,omitempty"`
	Mentionable bool     `yaml:"mentionable,omitempty"`
}
type AutoMod struct {
	Identity `yaml:",inline"`
	Name     string   `yaml:"name"`
	Keywords []string `yaml:"keywords"`
	Block    bool     `yaml:"block"`
	Alert    string   `yaml:"alert,omitempty"`
}
type Overwrites struct {
	Allow map[string][]string `yaml:"allow,omitempty"`
	Deny  map[string][]string `yaml:"deny,omitempty"`
}
type Category struct {
	Identity   `yaml:",inline"`
	Overwrites `yaml:",inline"`
	Sync       bool     `yaml:"sync"`
	Channels   []string `yaml:"channels,omitempty"`
}
type Channel struct {
	Identity   `yaml:",inline"`
	Overwrites `yaml:",inline"`
	Type       string   `yaml:"type,omitempty"`
	Topic      string   `yaml:"topic,omitempty"`
	Slowmode   string   `yaml:"slowmode,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
}
type Message struct {
	Title    string          `yaml:"title,omitempty"`
	Color    string          `yaml:"color,omitempty"`
	Footer   string          `yaml:"footer,omitempty"`
	Pin      bool            `yaml:"pin,omitempty"`
	Mentions string          `yaml:"mentions,omitempty"`
	Embed    json.RawMessage `yaml:"-"`
	Body     string          `yaml:"-"`
}
type Entry struct {
	Path      string
	Name      string
	Parent    string
	Category  *Category
	Channel   *Channel
	Messages  map[string]Message
	Unmanaged string
}

// Snapshot is configuration only, with adopted IDs and sidebar order.
type Snapshot struct {
	Server  Server
	Entries []Entry
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n") && name != "UNMANAGED.md"
}

type Folder struct {
	Server  Server
	Entries []Entry
	Policy  string
	Hooks   bool
}
