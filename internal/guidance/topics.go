package guidance

// Topic is one `dyna guide` topic and the trigger that says when to read it.
type Topic struct {
	Name    string
	Trigger string
}

// RootTopic is for root only. Dyna sessions have their room, get posts
// delivered and get rules injected, so they never see it listed.
const RootTopic = "external-orchestration"

// Topics is the one list of guide topics. The guide index and the session
// prompt both print it.
var Topics = []Topic{
	{"sessions", "session lifecycle and dormancy"},
	{"delegation", "before you write a brief; depth, schema results, goals and finishing"},
	{"workflows", "root's tool for large parallel jobs (sweeps, migrations, audits, ports)"},
	{"steering", "when a post arrives during your turn; replies, reports and silent turns"},
	{"posting", "before you attach files, post a card, pin a post or start a build, test run or dev server"},
	{"nodes", "before you use another node for builds or tests: run dyna node list; use the SSH column as the exact user@host"},
	{"git", "before you create a worktree, open, track, review or comment on a pull request, check CI, change merge policy, or manage hooks; also when a hook fails"},
	{"computers", "before you start a GUI app, take a screenshot or send computer input"},
	{"voice-agents", "to add speech or live voice to apps"},
	{"recovery", "to transfer, consolidate, mute or pause sessions, or after a model or provider failure"},
	{RootTopic, "root only: arm one watcher for root input blocks and load the rules"},
}
