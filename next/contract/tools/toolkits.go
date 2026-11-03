package tools

// Toolkit describes the MCP annotations shared by all actions in an area.
// Registration-only exposure follows T3 Code McpToolAccess.ts at 15fe283 (MIT).
type Toolkit struct {
	Name        string
	Title       string
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
	// Description opens the tool's description, before each action's help: what every action shares.
	Description string
	// Fields describes, by JSON name, the fields several of the tool's actions take, once. A field's own desc tag wins.
	Fields map[string]string
}

var toolkits = map[string]Toolkit{
	"computer":     {Name: "computer", Title: "Computer", Destructive: true, OpenWorld: true, Description: computerHelp, Fields: computerFields},
	"context":      {Name: "context", Title: "Context", Idempotent: true},
	"device":       {Name: "device", Title: "Devices", Destructive: true},
	"goal":         {Name: "goal", Title: "Goals"},
	"host":         {Name: "host", Title: "Hosts", Destructive: true, OpenWorld: true},
	"hostexec":     {Name: "hostexec", Title: "Host access", Destructive: true, OpenWorld: true},
	"issue":        {Name: "issue", Title: "Issues", OpenWorld: true},
	"mcp":          {Name: "mcp", Title: "MCP servers", Destructive: true, OpenWorld: true},
	"op":           {Name: "op", Title: "Operations", Destructive: true},
	"package":      {Name: "package", Title: "Pi packages", Destructive: true, OpenWorld: true},
	"pool":         {Name: "pool", Title: "Node pools", Destructive: true, OpenWorld: true},
	"post":         {Name: "post", Title: "Posts", Destructive: true},
	"pr":           {Name: "pr", Title: "Pull requests", Destructive: true, OpenWorld: true},
	"question":     {Name: "question", Title: "Questions for the owner"},
	"room":         {Name: "room", Title: "Rooms", Destructive: true},
	"session":      {Name: "session", Title: "Sessions", Destructive: true},
	"skill":        {Name: "skill", Title: "Skills", Destructive: true, OpenWorld: true},
	"subscription": {Name: "subscription", Title: "Subscriptions", Destructive: true},
	"visual":       {Name: "visual", Title: "Visual checks", Idempotent: true},
	"web":          {Name: "web", Title: "Web search", Idempotent: true, OpenWorld: true},
	"workflow":     {Name: "workflow", Title: "Workflows", Destructive: true, OpenWorld: true},
}

// ToolkitFor returns the registered metadata for an area.
func ToolkitFor(name string) (Toolkit, bool) { row, ok := toolkits[name]; return row, ok }
