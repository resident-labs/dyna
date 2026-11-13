package policy

import "strings"

type Resource string

const (
	Guild   Resource = "guilds"
	Channel Resource = "channels"
	User    Resource = "users"
)

type Action string

const (
	Unknown       Action = "unknown"
	Read          Action = "read"
	WebhookRead   Action = "webhookRead"
	MemberTimeout Action = "memberTimeout"
	MemberRole    Action = "memberRole"
	MessageCreate Action = "messageCreate"
	MessageEdit   Action = "messageEdit"
	PollExpire    Action = "pollExpire"
	MessageDelete Action = "messageDelete"
	Pin           Action = "pin"
	Reaction      Action = "reaction"
	ThreadCreate  Action = "threadCreate"
)

// Route is the effect classification shared by authorization and its live-fact lookups.
// ParseRoute accepts paths already normalized and resource-scoped by the engine.
type Route struct {
	Resource   Resource
	ResourceID string
	Action     Action
	RoleID     string
	MessageID  string
	Forum      bool
	Path       string
}

func ParseRoute(method, path string) Route {
	path, _, _ = strings.Cut(path, "?")
	parts := strings.Split(strings.TrimPrefix(path, "/api/v10/"), "/")
	route := Route{Action: Unknown, Path: path}
	if len(parts) < 2 {
		return route
	}
	route.Resource = Resource(parts[0])
	route.ResourceID = parts[1]
	if method == "GET" {
		route.Action = Read
		if len(parts) > 2 && parts[2] == "webhooks" {
			route.Action = WebhookRead
		}
		return route
	}
	if route.Resource == Guild {
		if len(parts) == 4 && parts[2] == "members" && method == "PATCH" {
			route.Action = MemberTimeout
		}
		if len(parts) == 6 && parts[2] == "members" && parts[4] == "roles" && (method == "PUT" || method == "DELETE") {
			route.Action = MemberRole
			route.RoleID = parts[5]
		}
		return route
	}
	if route.Resource != Channel || len(parts) < 3 {
		return route
	}
	if len(parts) == 3 && parts[2] == "threads" && method == "POST" {
		route.Action = ThreadCreate
		route.Forum = true
		return route
	}
	if len(parts) == 5 && parts[2] == "polls" && parts[4] == "expire" && method == "POST" {
		route.Action = PollExpire
		route.MessageID = parts[3]
		return route
	}
	if parts[2] != "messages" {
		return route
	}
	if len(parts) == 3 && method == "POST" {
		route.Action = MessageCreate
		return route
	}
	if len(parts) < 4 {
		return route
	}
	route.MessageID = parts[3]
	if len(parts) == 4 {
		switch method {
		case "PATCH":
			route.Action = MessageEdit
		case "DELETE":
			route.Action = MessageDelete
		}
		return route
	}
	if len(parts) == 5 && parts[3] == "pins" && (method == "PUT" || method == "DELETE") {
		route.Action = Pin
		route.MessageID = parts[4]
	}
	if len(parts) == 5 && parts[4] == "threads" && method == "POST" {
		route.Action = ThreadCreate
	}
	if len(parts) == 7 && parts[4] == "reactions" && parts[6] == "@me" && (method == "PUT" || method == "DELETE") {
		route.Action = Reaction
	}
	return route
}
