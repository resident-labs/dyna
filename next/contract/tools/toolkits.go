package tools

// Toolkit describes the MCP annotations shared by all actions in an area.
// Registration-only exposure follows T3 Code McpToolAccess.ts at 15fe283 (MIT).
type Toolkit struct {
	Name        string
	Title       string
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

var toolkits = map[string]Toolkit{
	"computer":     {Name: "computer", Title: "Computer", Destructive: true, OpenWorld: true},
	"context":      {Name: "context", Title: "Context", Idempotent: true},
	"device":       {Name: "device", Title: "Devices", Destructive: true},
	"goal":         {Name: "goal", Title: "Goals"},
	"host":         {Name: "host", Title: "Hosts", Destructive: true, OpenWorld: true},
	"hostexec":     {Name: "hostexec", Title: "Host commands", Destructive: true, OpenWorld: true},
	"op":           {Name: "op", Title: "Operations", Destructive: true},
	"post":         {Name: "post", Title: "Posts", Destructive: true},
	"pr":           {Name: "pr", Title: "Pull requests", Destructive: true, OpenWorld: true},
	"question":     {Name: "question", Title: "Questions for the owner"},
	"room":         {Name: "room", Title: "Rooms", Destructive: true},
	"session":      {Name: "session", Title: "Sessions", Destructive: true},
	"subscription": {Name: "subscription", Title: "Subscriptions", Destructive: true},
	"visual":       {Name: "visual", Title: "Visual checks", Idempotent: true},
	"web":          {Name: "web", Title: "Web search", Idempotent: true, OpenWorld: true},
	"workflow":     {Name: "workflow", Title: "Workflows", Destructive: true, OpenWorld: true},
}

// ToolkitFor returns the registered metadata for an area.
func ToolkitFor(name string) (Toolkit, bool) { row, ok := toolkits[name]; return row, ok }
