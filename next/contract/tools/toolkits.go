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
	"discord":      {Name: "discord", Title: "Discord", Destructive: true, OpenWorld: true, Description: "Available to a mapping's answering session and its ancestors. A whole-server mapping grants authority over every channel, including those answered by another room. A channel mapping grants its room that channel and its threads, but cannot administer the server. GET /users/ID is allowed; other roots and the bot's own account are refused. The engine holds the token; it never enters your box. Only the configured owner's Discord ID is the owner; everyone else is outside text. Discord renders no tables and no headings above level 3; write lists. See dyna_discord for mappings and dyna-discord for recipes."},
	"computer":     {Name: "computer", Title: "Computer", Destructive: true, OpenWorld: true, Description: computerHelp, Fields: computerFields},
	"advisor":      {Name: "advisor", Title: "Advisor"},
	"context":      {Name: "context", Title: "Context", Idempotent: true},
	"device":       {Name: "device", Title: "Devices", Destructive: true},
	"goal":         {Name: "goal", Title: "Goals", Description: "item, done and journal act on the goal you hold when goal is omitted."},
	"host":         {Name: "host", Title: "Hosts", Destructive: true, OpenWorld: true},
	"hostexec":     {Name: "hostexec", Title: "Host access", Destructive: true, OpenWorld: true},
	"issue":        {Name: "issue", Title: "Issues", OpenWorld: true},
	"mcp":          {Name: "mcp", Title: "MCP servers", Destructive: true, OpenWorld: true},
	"op":           {Name: "op", Title: "Operations", Destructive: true},
	"package":      {Name: "package", Title: "Pi packages", Destructive: true, OpenWorld: true},
	"pool":         {Name: "pool", Title: "Node pools", Destructive: true, OpenWorld: true},
	"post":         {Name: "post", Title: "Posts", Destructive: true},
	"pr":           {Name: "pr", Title: "Pull requests", Destructive: true, OpenWorld: true, Description: prHelp},
	"question":     {Name: "question", Title: "Questions for the owner"},
	"room":         {Name: "room", Title: "Rooms", Destructive: true},
	"session":      {Name: "session", Title: "Sessions", Destructive: true},
	"image":        {Name: "image", Title: "Room images", Destructive: true},
	"skill":        {Name: "skill", Title: "Skills", Destructive: true, OpenWorld: true},
	"subscription": {Name: "subscription", Title: "Subscriptions", Destructive: true, Fields: map[string]string{"key": "Workflow key: add and update take a literal or template using {repo} and {number}; omitted means one key per subscription. fire overrides the resolved key with a literal; state reads that literal key, or lists keys when omitted.", "where": "Filters on github.* and git.* events: key=value, key!=value or text~value on repo, author, actor, label, kind or number. All must hold; kind=a,b matches any. update replaces the list; an empty list clears it.", "workflow": "Saved JavaScript source as script and an args object. Root or owner only, room scope. On update, replaces script and args without losing state or queue.", "latest": "Keep only the newest waiting event for a key; the running run is never replaced.", "parent": "The session the run reports to and that may steer it; root by default, or when it has closed."}},
	"visual":       {Name: "visual", Title: "Visual checks", Idempotent: true},
	"web":          {Name: "web", Title: "Web search", Idempotent: true, OpenWorld: true},
	"search":       {Name: "search", Title: "Search Dyna", Idempotent: true, Description: "Search Dyna. Feed posts, sessions and rooms are searched across rooms; a DM post is found only in your own room. Another room's snippets come wrapped in <outside>. Every action takes room (a name or ID; omit for all rooms, here for yours), limit (at most 25) and after (a result's next, for the rest).", Fields: map[string]string{"query": "1 to 8 words, each a prefix.", "room": "A room name or ID; here is yours; omit for all rooms.", "after": "A result's next.", "limit": "At most 25; default 10."}},
	"history":      {Name: "history", Title: "Conversations", Idempotent: true},
	"transcript":   {Name: "transcript", Title: "Transcripts", Idempotent: true},
	"workflow":     {Name: "workflow", Title: "Workflows", Destructive: true, OpenWorld: true},
}

// ToolkitFor returns the registered metadata for an area.
func ToolkitFor(name string) (Toolkit, bool) { row, ok := toolkits[name]; return row, ok }

// prHelp opens the pr tool's description, for every action. It is the one place that says how CI, tracking and merging work.
const prHelp = "CI is GitHub Actions, from each repository's .github/workflows: tests in a box show a change works there, CI shows it on GitHub. A PR you open is tracked for you, and track adds others. The engine polls tracked PRs and wakes the session that owns one on CI results, reviews, comments, conflicts, merges and closes; dyna_holds lists yours. Opening a PR merges nothing. Boxes have no gh. Reading GitHub needs no host: the mcp__github__ tools read PRs, runs and CI job logs (get_job_logs). Comments and merges run on a host through host exec, by root or a session granted it, as the room's rules say."
