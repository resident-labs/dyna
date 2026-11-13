package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Write checks a normalized Discord route. Resource scope remains the caller's responsibility.
// OwnMessage is fetched from Discord, rather than trusted from the request body.
type Facts struct {
	Now             time.Time
	OwnMessage      bool
	RolePermissions uint64
}

const DangerousPermissions uint64 = 1<<3 | 1<<5 | 1<<28 | 1<<4 | 1<<1 | 1<<2 | 1<<40 | 1<<29 | 1<<17

func bodyInto(body json.RawMessage, out any) error {
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	if err := exactJSON(json.NewDecoder(bytes.NewReader(body)), reflect.TypeOf(out)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

type Mentions struct {
	Parse       []string `json:"parse,omitempty"`
	Users       []string `json:"users,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	RepliedUser bool     `json:"replied_user,omitempty"`
}
type Message struct {
	Content   string    `json:"content,omitempty"`
	Mentions  *Mentions `json:"allowed_mentions,omitempty"`
	Reference *struct {
		Message string `json:"message_id"`
		Channel string `json:"channel_id,omitempty"`
		Guild   string `json:"guild_id,omitempty"`
		Fail    *bool  `json:"fail_if_not_exists,omitempty"`
	} `json:"message_reference,omitempty"`
}

func safeMessage(m Message) bool {
	if m.Mentions == nil {
		return false
	}
	if len(m.Mentions.Roles) > 0 {
		return false
	}
	for _, s := range m.Mentions.Parse {
		if s != "users" {
			return false
		}
	}
	return true
}
func (p Policy) Write(method, path string, body json.RawMessage, f Facts) error {
	deny := func(cap string) error {
		return fmt.Errorf("the server's policy limits the answering session's %s; ask a moderator or your parent", cap)
	}
	path, _, _ = strings.Cut(path, "?")
	a := strings.Split(strings.TrimPrefix(path, "/api/v10/"), "/")
	if method == "GET" {
		if len(a) > 2 && a[2] == "webhooks" {
			return deny("webhook reads")
		}
		return nil
	}
	if len(a) < 3 {
		return deny("writes")
	}
	if a[0] == "guilds" {
		if len(a) == 4 && a[2] == "members" && method == "PATCH" {
			var b struct {
				Until json.RawMessage `json:"communication_disabled_until"`
			}
			if bodyInto(body, &b) != nil || len(b.Until) == 0 {
				return deny("timeout fields")
			}
			if string(b.Until) == "null" {
				return nil
			}
			var text string
			if json.Unmarshal(b.Until, &text) != nil {
				return deny("timeout_max")
			}
			until, e := time.Parse(time.RFC3339Nano, text)
			max, _ := time.ParseDuration(p.Front.TimeoutMax)
			if e != nil || max == 0 || until.After(f.Now.Add(max)) {
				return deny("timeout_max of " + p.Front.TimeoutMax)
			}
			return nil
		}
		if len(a) == 6 && a[2] == "members" && a[4] == "roles" && (method == "PUT" || method == "DELETE") {
			var empty struct{}
			if bodyInto(body, &empty) != nil {
				return deny("role fields")
			}
			for _, r := range p.Front.Roles {
				if r == a[5] && f.RolePermissions&DangerousPermissions == 0 {
					return nil
				}
			}
			return deny("roles and safe role permissions")
		}
		return deny("writes")
	}
	if a[0] != "channels" {
		return deny("writes")
	}
	if a[2] == "messages" {
		if len(a) == 3 && method == "POST" {
			var m Message
			if bodyInto(body, &m) != nil || !safeMessage(m) {
				return deny("message fields and allowed_mentions")
			}
			return nil
		}
		if len(a) == 4 && method == "PATCH" {
			var m Message
			if bodyInto(body, &m) != nil || m.Reference != nil || !safeMessage(m) || !f.OwnMessage {
				return deny("editing its own message")
			}
			return nil
		}
		if len(a) == 4 && method == "DELETE" {
			id, e := strconv.ParseUint(a[3], 10, 64)
			created := time.UnixMilli(int64(id>>22) + 1420070400000)
			var empty struct{}
			if !bool(p.Front.Delete) || e != nil || created.Before(f.Now.Add(-14*24*time.Hour)) || bodyInto(body, &empty) != nil {
				return deny("delete of one message under 14 days")
			}
			return nil
		}
		if len(a) == 5 && a[3] == "pins" && (method == "PUT" || method == "DELETE") {
			var empty struct{}
			if bodyInto(body, &empty) == nil {
				return nil
			}
		}
		if len(a) == 6 && a[4] == "threads" {
			return deny("writes")
		}
		if len(a) == 5 && a[4] == "threads" && method == "POST" {
			return p.thread(body, false, deny)
		}
		if len(a) >= 5 && a[4] == "reactions" && (method == "PUT" || method == "DELETE") {
			var empty struct{}
			if len(a) <= 7 && bodyInto(body, &empty) == nil {
				return nil
			}
		}
	}
	if len(a) == 3 && a[2] == "threads" && method == "POST" {
		return p.thread(body, true, deny)
	}
	return deny("writes")
}
func (p Policy) thread(body json.RawMessage, forum bool, deny func(string) error) error {
	if !p.Front.Threads {
		return deny("threads")
	}
	var b struct {
		Name      string   `json:"name"`
		Archive   int      `json:"auto_archive_duration,omitempty"`
		Type      int      `json:"type,omitempty"`
		Invitable *bool    `json:"invitable,omitempty"`
		Message   *Message `json:"message,omitempty"`
		Tags      []string `json:"applied_tags,omitempty"`
	}
	if bodyInto(body, &b) != nil || b.Name == "" || (!forum && (b.Message != nil || len(b.Tags) > 0)) || (b.Message != nil && !safeMessage(*b.Message)) {
		return deny("thread fields")
	}
	return nil
}
