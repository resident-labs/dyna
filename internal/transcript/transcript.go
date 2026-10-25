// Package transcript turns a worker's normalized output into a readable
// conversation: prompts, steering, replies, and tool activity condensed into
// one-line summaries ("ran `go test ./...` (12s, exit 0)", "edited 3 files").
// The desktop session view, the runs Output view, and `dyna subagent show` render
// from it, so no human-facing view starts from raw harness output.
package transcript

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/textutil"
	"dyna-agent/internal/workeroutput"
)

// Kind is what an item is.
type Kind int

const (
	Prompt     Kind = iota // what the user (or parent) asked
	Steering               // a message delivered into a running turn
	Reply                  // the agent's prose
	ToolUse                // one condensed tool line (possibly a group)
	Thought                // a one-line reasoning summary
	Error                  // a failure worth reading
	Note                   // a quiet system note
	Compaction             // the harness compacting the conversation's context
)

// CompactionState is where a compaction is now.
type CompactionState string

const (
	CompactionStarted   CompactionState = "started"
	CompactionCompleted CompactionState = "completed"
	CompactionFailed    CompactionState = "failed"
)

// CompactionInfo is one compaction of the conversation's context, as the
// harness reported it. Trigger is "auto", "manual" or "dyna" when Dyna ran
// the harness's compact command itself.
type CompactionInfo struct {
	State   CompactionState
	Trigger string
	Reason  string // why it failed
	Before  int64  // context size before it, when reported
	After   int64  // context size after it, when reported
}

// Tool is a condensed tool call (or a run of similar ones).
type Tool struct {
	// Step is what the call did, in the session view's own words. The web picks
	// the step's icon and its state chip from it, never from Verb.
	Step StepKind
	// Name is the harness's own tool ID, such as "mcp__dyna__shell". Only a
	// step's expanded details show it.
	Name    string
	Verb    string // "ran", "read", "edited", "searched for", "started a dyna workflow", …
	Target  string // the command, file, or query (shown as code)
	Detail  string // the full input, for the expanded view
	Output  string // the call's output, when the harness reported it
	Exit    *int   // exit code, when reported
	Failed  bool   // the call failed
	Done    bool   // a completion was seen
	Dur     time.Duration
	Count   int      // >1 when consecutive similar calls were grouped
	Targets []string // grouped targets
	// JobID and JobOp link a call of Dyna's shell tools to its shell job. JobOp
	// is "run" (shell), "output" (shell_output) or "stop" (shell_stop).
	// Description is the shell call's one-line title.
	JobID, JobOp, Description string
}

// StepKind is the kind of work one tool call did. It is a closed set, so the
// session view draws a designed row for each kind instead of showing a tool ID.
type StepKind string

// Step kinds. StepTool covers every tool the view names, such as an MCP tool.
const (
	StepRead     StepKind = "read"
	StepEdit     StepKind = "edit"
	StepWrite    StepKind = "write"
	StepSearch   StepKind = "search"
	StepList     StepKind = "list"
	StepRun      StepKind = "run"
	StepFetch    StepKind = "fetch"
	StepDelegate StepKind = "delegate"
	StepPlan     StepKind = "plan"
	StepMonitor  StepKind = "monitor"
	StepTool     StepKind = "tool"
)

// Item is one entry of the conversation.
type Item struct {
	source    int64
	anchor    *int64
	Kind      Kind
	PostID    string    // original room post
	TS        time.Time // zero when the source carried no time
	Text      string    // markdown for Prompt/Steering/Reply; plain otherwise
	From      string    // steering sender, when known
	Status    string    // steering delivery state
	Delivered bool
	Tool      Tool
	// Compaction is set when Kind is Compaction.
	Compaction CompactionInfo
}

// Turn is one turn of a subagent session (or one workflow worker call).
type Turn struct {
	WrapUp             bool
	Harness            string
	OutputDialect      string
	UsageNormalization workeroutput.UsageNormalization
	Profile            string
	Model              string
	HarnessSession     string
	N                  int
	Status             string
	Started            time.Time
	Ended              time.Time
	Usage              workeroutput.Usage
	Items              []Item
	Hidden             int  // unparsed harness records left out (the raw view has them)
	Omitted            bool // earlier events were dropped from memory
}

// Running reports whether the turn has not ended.
func (t Turn) Running() bool {
	return t.Status == subagent.TurnRunning || t.Status == subagent.TurnQueued
}

// Duration is the turn's wall time so far.
func (t Turn) Duration(now time.Time) time.Duration {
	switch {
	case t.Started.IsZero():
		return 0
	case t.Ended.IsZero():
		return now.Sub(t.Started)
	}
	return t.Ended.Sub(t.Started)
}

// ---- events → items --------------------------------------------------------

// FromEvents condenses normalized events into items. hidden counts records
// that carry nothing readable (unparsed raw lines, plumbing statuses that
// are errors-in-name-only are kept).
func FromEvents(events []workeroutput.Event) (items []Item, hidden int) {
	items, hidden, _ = fromEvents(events)
	return items, hidden
}

// fromEvents also returns the item indexes where a resumed continuation
// starts (a new harness process: steering or a reminder was delivered).
func fromEvents(events []workeroutput.Event) ([]Item, int, []int) {
	return fromEventsWithSteering(events, nil)
}

// fromEventsWithSteering inserts source boundaries before coalescing. Tool
// results can still pair with starts across a boundary, while adjacent prose,
// thoughts and tool groups cannot absorb the steering between them.
func fromEventsWithSteering(events []workeroutput.Event, steers []Item) ([]Item, int, []int) {
	if len(steers) > 0 {
		ordered := append([]Item(nil), steers...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].anchor == nil {
				return false
			}
			return ordered[j].anchor == nil || *ordered[i].anchor < *ordered[j].anchor
		})
		steers = ordered
		merged := make([]workeroutput.Event, 0, len(events)+len(steers))
		next := 0
		insert := func() {
			merged = append(merged, workeroutput.Event{Kind: "transcript_steering", ID: strconv.Itoa(next)})
			next++
		}
		for _, e := range events {
			for next < len(steers) && steers[next].anchor != nil && *steers[next].anchor <= e.SourceOffset {
				insert()
			}
			merged = append(merged, e)
		}
		for next < len(steers) {
			insert()
		}
		events = merged
	}
	events = workeroutput.CoalesceNormalizedEvents(events)
	paired, ended := workeroutput.PairTools(events)
	var items []Item
	var conts []int
	hidden := 0
	starts := 0
	deltaReply, deltaID := -1, ""
	for i, e := range paired {
		ts := time.Time{}
		if e.TS > 0 {
			ts = time.UnixMilli(e.TS)
		}
		switch e.Kind {
		case "transcript_steering":
			if index, err := strconv.Atoi(e.ID); err == nil && index >= 0 && index < len(steers) {
				items = append(items, steers[index])
				deltaReply = -1
			}
		case "message":
			if strings.EqualFold(e.Role, "user") {
				continue // prompts and steering come from the turn record
			}
			text := e.Text
			if e.State == "delta" {
				if text == "" {
					continue
				}
				n := len(items)
				if deltaReply == n-1 && n > 0 && items[n-1].Kind == Reply && deltaID == e.ID {
					items[n-1].Text += text
					continue
				}
				items = append(items, Item{Kind: Reply, TS: ts, Text: text, source: e.SourceOffset})
				deltaReply, deltaID = len(items)-1, e.ID
				continue
			}
			deltaReply = -1
			text = strings.TrimRight(text, "\n")
			if strings.TrimSpace(text) == "" {
				continue
			}
			if repeated(items, text) {
				// A worker asked to journal before finishing repeats its answer;
				// the conversation shows it once.
				continue
			}
			if n := len(items); n > 0 && items[n-1].Kind == Reply {
				items[n-1].Text += "\n\n" + text
				continue
			}
			items = append(items, Item{Kind: Reply, TS: ts, Text: text, source: e.SourceOffset})
		case "reasoning":
			if line := textutil.FirstLine(stripEmphasis(e.Text)); line != "" {
				// A run of thoughts reads as one line: the latest, with a count.
				if n := len(items); n > 0 && items[n-1].Kind == Thought {
					items[n-1].Text, items[n-1].TS = line, ts
					items[n-1].Tool.Count = max(items[n-1].Tool.Count, 1) + 1
					continue
				}
				items = append(items, Item{Kind: Thought, TS: ts, Text: line, source: e.SourceOffset})
			}
		case "tool":
			if e.Name == "tool_result" && e.ID != "" {
				continue // an orphan completion: its start was not kept
			}
			t := Summarize(e)
			if end, ok := ended[i]; ok && end > 0 && e.TS > 0 {
				t.Dur = time.Duration(end-e.TS) * time.Millisecond
			}
			items = appendTool(items, Item{Kind: ToolUse, TS: ts, Tool: t, source: e.SourceOffset})
		case "status":
			if e.Name == "response" || e.Name == "result" || e.State == "started" {
				deltaReply = -1
			}
			switch {
			// TODO: legacy/shim remove later — compact_boundary names Claude compaction in output written before the compaction status shape; remove after those artifacts expire
			case e.Name == "compaction" || e.Name == "compact_boundary":
				items = appendCompaction(items, e, ts)
			case e.Name == "thread" || e.Name == "session" || (e.Name == "turn" && e.State == "started"):
				if e.State == "started" {
					if (e.Name == "thread" || e.Name == "session") && starts > 0 {
						conts = append(conts, len(items))
					}
					if e.Name == "thread" || e.Name == "session" {
						starts++
					}
				}
			case e.State == "failed" || e.Name == "error":
				text := strings.TrimSpace(e.Text)
				if text == "" || noise(text) {
					hidden++
					continue
				}
				items = append(items, Item{Kind: Error, TS: ts, Text: text, source: e.SourceOffset})
			}
		case "raw":
			hidden++
		}
	}
	return items, hidden, conts
}

// appendCompaction adds one compaction item. A start that arrives while a
// compaction is already running is the same compaction reported again, and the
// outcome closes the open item wherever other work sits between them.
func appendCompaction(items []Item, e workeroutput.Event, ts time.Time) []Item {
	c := CompactionInfo{State: CompactionState(e.State), Trigger: e.Trigger, Before: e.TokensBefore, After: e.TokensAfter}
	if c.State == CompactionFailed {
		c.Reason = strings.TrimSpace(e.Text)
	}
	open := -1
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == Compaction && items[i].Compaction.State == CompactionStarted {
			open = i
			break
		}
	}
	if c.State == CompactionStarted {
		if open >= 0 {
			return items
		}
		return append(items, Item{Kind: Compaction, TS: ts, Compaction: c, source: e.SourceOffset})
	}
	if open >= 0 {
		items[open].Compaction, items[open].TS = c, ts
		return items
	}
	return append(items, Item{Kind: Compaction, TS: ts, Compaction: c, source: e.SourceOffset})
}

// repeated reports whether text is a full reply already given in items.
func repeated(items []Item, text string) bool {
	t := strings.TrimSpace(text)
	if len(t) < 40 {
		return false
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind == Steering {
			break
		}
		if it.Kind == Reply && strings.TrimSpace(it.Text) == t {
			return true
		}
	}
	return false
}

// noise is harness chatter that is not about the task.
func noise(text string) bool {
	return strings.HasPrefix(text, "Under-development features enabled") ||
		text == "`--dangerously-bypass-hook-trust` is enabled. Enabled hooks may run without review for this invocation."
}

// appendTool groups consecutive reads and edits into one line.
func appendTool(items []Item, it Item) []Item {
	n := len(items)
	if n > 0 && items[n-1].Kind == ToolUse {
		prev := &items[n-1].Tool
		// A shell call is its own terminal block, even when its command edits a file.
		if groupable(prev.Verb) && prev.Verb == it.Tool.Verb && !it.Tool.Failed && !prev.Failed && prev.JobOp == "" && it.Tool.JobOp == "" {
			if prev.Count == 0 {
				prev.Count, prev.Targets = 1, []string{prev.Target}
			}
			prev.Count++
			prev.Targets = append(prev.Targets, it.Tool.Target)
			prev.Dur += it.Tool.Dur
			prev.Done = prev.Done && it.Tool.Done
			prev.Detail = strings.TrimSpace(prev.Detail + "\n" + it.Tool.Detail)
			return items
		}
	}
	return append(items, it)
}

func groupable(verb string) bool {
	switch verb {
	case "read", "edited", "wrote", "listed", "searched for":
		return true
	}
	return false
}

// ---- tool summaries ----------------------------------------------------------

var shellWrap = regexp.MustCompile(`(?s)^\S*/?(?:ba|z|da)?sh\s+-l?c\s+(['"])(.*)['"]\s*$`)

// UnwrapShell removes a harness's `bash -lc '…'` wrapper.
func UnwrapShell(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if m := shellWrap.FindStringSubmatch(cmd); m != nil {
		inner := m[2]
		if m[1] == `"` {
			inner = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`").Replace(inner)
		} else {
			inner = strings.ReplaceAll(inner, `'"'"'`, `'`)
			inner = strings.ReplaceAll(inner, `'\''`, `'`)
		}
		return strings.TrimSpace(inner)
	}
	return cmd
}

var (
	runIDPattern = regexp.MustCompile(`wf_\d{8}-\d{6}-[0-9a-f]{8}`)
	flagName     = regexp.MustCompile(`--name[ =]+(\S+)`)
)

// dynaShellOp names the job tool of Dyna's MCP server that a tool name calls,
// in each harness's spelling, for example mcp__dyna__shell, dyna_shell or
// dyna.shell. It returns "run", "output", "stop", or "" for any other tool.
func dynaShellOp(lower string) string {
	name := strings.NewReplacer("__", "_", ".", "_", "/", "_", "-", "_", ":", "_").Replace(lower)
	for _, tool := range []struct{ suffix, op string }{{"dyna_shell", "run"}, {"dyna_shell_output", "output"}, {"dyna_shell_stop", "stop"}} {
		if name == tool.suffix || strings.HasSuffix(name, "_"+tool.suffix) {
			return tool.op
		}
	}
	return ""
}

// jobLead finds the job in a result that starts "Job <id>." (see
// shellmcp.exitLine). Results from before that lead name no job.
var jobLead = regexp.MustCompile(`^\s*Job (sh_\d{8}-\d{6}-[0-9a-f]{8})\b`)

// mcpResultText extracts the first text block from an MCP result envelope.
// Plain results and invalid envelopes keep their original interpretation.
func mcpResultText(output string) string {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(output), &result) == nil {
		for _, block := range result.Content {
			if block.Type == "text" {
				return block.Text
			}
		}
	}
	return output
}

// Summarize condenses one tool event.
func Summarize(e workeroutput.Event) Tool {
	// TODO: legacy/shim remove later — renders old normalized Muse envelopes; remove after affected artifacts expire or migrate
	e = workeroutput.UpgradeLegacyEvent(e)
	t := Tool{Name: e.Name, Detail: e.Input, Output: e.Output, Exit: e.Exit}
	t.Done = e.State == "completed" || e.State == "failed"
	t.Failed = e.State == "failed" || (e.Exit != nil && *e.Exit != 0)
	args := map[string]any{}
	if strings.HasPrefix(strings.TrimSpace(e.Input), "{") {
		_ = json.Unmarshal([]byte(e.Input), &args)
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	name := e.Name
	lower := strings.ToLower(name)
	switch {
	case lower == "command" || lower == "bash" || lower == "shell" || lower == "run_command" || lower == "run_terminal_cmd" || dynaShellOp(lower) == "run":
		cmd := e.Input
		if c := str("command", "CommandLine", "cmd"); c != "" {
			cmd = c
		}
		t.Detail = UnwrapShell(cmd)
		t.Verb, t.Target = shellSummary(t.Detail, e.Output)
		if dynaShellOp(lower) == "run" {
			t.Description, t.Step = str("description"), StepRun
		}
	case lower == "read" || lower == "read_file" || lower == "view_file":
		t.Verb, t.Step, t.Target = "read", StepRead, shortPath(str("file_path", "path", "target_file", "AbsolutePath"))
	case lower == "edit" || lower == "multiedit" || lower == "edit_file" || lower == "replace_file_content" || lower == "search_replace":
		t.Verb, t.Step, t.Target = "edited", StepEdit, shortPath(str("file_path", "path", "target_file", "TargetFile"))
	case lower == "write" || lower == "write_file" || lower == "write_to_file":
		t.Verb, t.Step, t.Target = "wrote", StepWrite, shortPath(str("file_path", "path", "target_file", "TargetFile"))
	case lower == "file_change":
		var paths []string
		for _, line := range strings.Split(strings.TrimSpace(e.Input), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				paths = append(paths, shortPath(f[len(f)-1]))
			}
		}
		t.Verb, t.Step = "edited", StepEdit
		if len(paths) == 1 {
			t.Target = paths[0]
		} else if len(paths) > 1 {
			t.Target, t.Count, t.Targets = paths[0], len(paths), paths
		}
	case lower == "grep" || lower == "grep_search" || lower == "codebase_search" || lower == "search":
		t.Verb, t.Step, t.Target = "searched for", StepSearch, str("pattern", "query", "Query")
	case lower == "glob" || lower == "list_dir" || lower == "ls" || lower == "find_by_name":
		t.Verb, t.Step, t.Target = "listed", StepList, str("pattern", "path", "SearchDirectory", "Pattern")
	case lower == "web_search" || lower == "websearch" || lower == "search_web":
		t.Verb, t.Step, t.Target = "searched the web", StepSearch, str("query", "Query")
		if t.Target == "" && !strings.HasPrefix(e.Input, "{") {
			t.Target = strings.TrimSpace(e.Input)
		}
	case lower == "webfetch" || lower == "web_fetch" || lower == "read_url_content":
		t.Verb, t.Step, t.Target = "fetched", StepFetch, str("url", "Url")
	case lower == "task" || lower == "agent":
		t.Verb, t.Step, t.Target = "delegated to a subagent", StepDelegate, str("description", "subagent_type")
	case lower == "todowrite" || lower == "write_todos" || lower == "update_plan":
		t.Verb, t.Step = "updated the plan", StepPlan
	case lower == "monitor":
		t.Verb, t.Step, t.Target = "started a monitor", StepMonitor, str("command", "description")
	case lower == "toolsearch":
		t.Verb, t.Step = "loaded tools", StepTool
	case lower == "tool" || lower == "bash_input":
		cmd := str("command")
		if cmd != "" {
			t.Detail = UnwrapShell(cmd)
			t.Verb, t.Target = shellSummary(t.Detail, t.Output)
		} else {
			t.Verb, t.Step = "used "+name, StepTool
			t.Target = str("file_path", "path", "query", "pattern", "description")
		}
	case strings.HasPrefix(name, "mcp__") || strings.Contains(name, "/"):
		t.Verb, t.Step, t.Target = "called", StepTool, strings.ReplaceAll(strings.TrimPrefix(name, "mcp__"), "__", "/")
	default:
		t.Verb, t.Step = "used "+name, StepTool
		t.Target = str("command", "file_path", "path", "query", "pattern", "description")
	}
	if t.Target == "" && t.Verb == "ran" {
		t.Target = textutil.FirstLine(e.Input)
	}
	if t.JobOp = dynaShellOp(lower); t.JobOp == "run" {
		if m := jobLead.FindStringSubmatch(mcpResultText(e.Output)); m != nil {
			t.JobID = m[1]
		}
	} else if t.JobOp != "" && shells.ValidID(str("job_id")) {
		t.JobID = str("job_id")
	}
	// A verb left without anything it acted on still reads as a sentence.
	switch {
	case t.Verb == "ran" && t.Target == "":
		t.Verb = "ran a command"
	case t.Verb == "searched for" && t.Target == "":
		t.Verb = "searched"
	}
	return t
}

// shellSummary recognizes dyna's own commands; anything else "ran".
func shellSummary(cmd, output string) (verb, target string) {
	line := textutil.FirstLine(cmd)
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"cd ", "export "} {
		if strings.HasPrefix(trimmed, prefix) {
			if i := strings.Index(trimmed, "&&"); i > 0 {
				trimmed = strings.TrimSpace(trimmed[i+2:])
			}
		}
	}
	fields := shellCommandArgs(trimmed)
	if len(fields) >= 2 && (fields[0] == "dyna" || strings.HasSuffix(fields[0], "/dyna") || fields[0] == "$DYNA_BIN") {
		// Help prints usage without doing the command's action.
		for _, arg := range fields[1:] {
			if arg == "--" {
				break
			}
			if arg == "--help" || arg == "-h" {
				return "ran", textutil.Clip(trimmed, 160)
			}
		}
		sub := strings.Join(fields[1:min(3, len(fields))], " ")
		switch {
		case fields[1] == "journal":
			return "journaled", quotedArg(trimmed)
		case fields[1] == "run":
			id := runIDPattern.FindString(output)
			if id == "" {
				id = scriptArg(fields[2:])
			}
			return "started a dyna workflow", id
		case strings.HasPrefix(sub, "subagent start"):
			name := argAt(strings.Fields(textutil.FirstLine(output)), 0)
			if !strings.Contains(name, ":") {
				name = ""
			}
			if name == "" {
				if m := flagName.FindStringSubmatch(trimmed); m != nil {
					name = strings.Trim(m[1], `'"`)
				}
			}
			return "started a subagent", name
		case fields[1] == "post":
			return "messaged", argAt(fields, 2)
		// TODO: legacy/shim remove later — recognizes the old dyna msg command in retained transcripts; remove after affected artifacts expire or migrate
		case fields[1] == "msg":
			return "messaged", argAt(fields, 2)
		case fields[1] == "shell" && len(fields) > 2 && fields[2] == "start":
			return "started a background shell", textutil.Clip(trimmed, 120)
		}
	}
	if strings.HasPrefix(trimmed, "apply_patch") {
		return "edited", "files"
	}
	if m := sshWrap.FindStringSubmatch(textutil.FirstLine(cmd)); m != nil {
		inner := m[3]
		if inner == "" {
			inner = m[4]
		}
		host := m[1]
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		inner = strings.ReplaceAll(strings.ReplaceAll(inner, `'\''`, `'`), `'"'"'`, `'`)
		return "ran on " + host, textutil.Clip(strings.TrimRight(strings.TrimSpace(inner), `'"\`), 160)
	}
	target = textutil.Clip(trimmed, 160)
	if n := strings.Count(strings.TrimSpace(cmd), "\n"); n > 0 {
		target += fmt.Sprintf("  (+%d lines)", n)
	}
	return "ran", target
}

func argAt(fields []string, i int) string {
	if i < len(fields) {
		return strings.Trim(fields[i], `'"`)
	}
	return ""
}

func scriptArg(fields []string) string {
	for _, f := range fields {
		if strings.HasSuffix(f, ".js") {
			return filepath.Base(strings.Trim(f, `'"`))
		}
	}
	return ""
}

// shellCommandArgs reads literal arguments from the first shell command.
// Quotes and escapes keep words together; operators end the scan.
func shellCommandArgs(cmd string) []string {
	var args []string
	var word strings.Builder
	started := false
	var quote rune
	escaped := false
	for _, c := range cmd + " " {
		if escaped {
			if c != '\n' {
				started = true
				word.WriteRune(c)
			}
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			started = true
			quote = c
			continue
		}
		boundary := strings.ContainsRune("|;&<>()`", c)
		if boundary || strings.ContainsRune(" \t\r\n", c) {
			if started {
				args = append(args, word.String())
			}
			if boundary {
				return args
			}
			word.Reset()
			started = false
			continue
		}
		started = true
		word.WriteRune(c)
	}
	return args
}

var sshWrap = regexp.MustCompile(`^ssh\s+(?:-\S+\s+)*([\w.@-]+)\s+(['"])(?:([^'"]+)|(.*))$`)

var quoted = regexp.MustCompile(`'([^']*)'|"((?:[^"\\]|\\.)*)"`)

func quotedArg(s string) string {
	if m := quoted.FindStringSubmatch(s); m != nil {
		if m[1] != "" {
			return textutil.Clip(m[1], 160)
		}
		return textutil.Clip(m[2], 160)
	}
	return ""
}

func shortPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	dir, base := filepath.Split(filepath.Clean(p))
	parent := filepath.Base(dir)
	if parent == "." || parent == "/" || parent == "" {
		return base
	}
	return parent + "/" + base
}

func stripEmphasis(s string) string {
	return strings.NewReplacer("**", "", "__", "").Replace(s)
}

// ---- session turns ---------------------------------------------------------

var framedMessage = regexp.MustCompile(`^\[dyna message (\S+) from ([^\]\n]+)\]\n`)

// SplitSender separates a post's envelope ("[dyna message <id> from X]" plus
// its To and Reply lines) from the post text.
func SplitSender(msg string) (from, text string) {
	_, from, text = splitPost(msg)
	return from, text
}

func splitPost(msg string) (id, from, text string) {
	msg = strings.TrimSpace(msg)
	if m := framedMessage.FindStringSubmatchIndex(msg); m != nil && m[0] == 0 {
		text = msg[m[1]:]
		for {
			line, rest, ok := strings.Cut(text, "\n")
			if !ok || !(strings.HasPrefix(line, "To: ") || strings.HasPrefix(line, "Reply: ")) {
				break
			}
			text = rest
		}
		return msg[m[2]:m[3]], msg[m[4]:m[5]], strings.TrimSpace(text)
	}
	return "", "", msg
}

func promptItems(t subagent.Turn, ts time.Time) []Item {
	prompt := strings.TrimSpace(t.Prompt)
	remaining := prompt
	var items []Item
	for remaining != "" {
		found := false
		for _, steer := range t.Steering {
			frame := strings.TrimSpace(steer.Message)
			if frame == "" || !strings.HasPrefix(remaining, frame) {
				continue
			}
			rest := remaining[len(frame):]
			if rest != "" && !strings.HasPrefix(rest, "\n\n") {
				continue
			}
			id, from, text := splitPost(frame)
			items = append(items, Item{Kind: Prompt, PostID: id, TS: ts, From: from, Text: text, Delivered: steer.Delivered})
			remaining = strings.TrimSpace(rest)
			found = true
			break
		}
		if !found {
			// Without complete stored frames, keep the input intact. Headers
			// inside quoted text cannot establish a new message boundary.
			id, from, text := splitPost(prompt)
			return []Item{{Kind: Prompt, PostID: id, TS: ts, From: from, Text: text}}
		}
	}
	if len(items) == 0 {
		return []Item{{Kind: Prompt, TS: ts}}
	}
	return items
}

// SessionTurn builds the conversation of one subagent turn: its prompt, the
// condensed events, steering placed where it arrived, and the result or error.
func SessionTurn(t subagent.Turn, events []workeroutput.Event, omitted bool) Turn {
	out := Turn{WrapUp: t.WrapUp, Harness: t.Harness, OutputDialect: t.OutputDialect, UsageNormalization: t.UsageNormalization, Profile: t.Profile, Model: t.Model, HarnessSession: t.HarnessSession, N: t.N, Status: t.Status, Started: t.Started, Ended: t.Ended, Usage: t.Usage, Omitted: omitted}
	promptTS := t.Created
	if !t.Started.IsZero() {
		promptTS = t.Started
	}
	// TODO: legacy/shim remove later — marks deltas in old normalized Grok and Muse output; remove after affected artifacts expire or migrate
	if t.OutputDialect == workeroutput.DialectGrokJSON || t.OutputDialect == workeroutput.DialectMuseJSONL {
		events = append([]workeroutput.Event(nil), events...)
		for i := range events {
			if events[i].Kind == "message" && events[i].Role == "assistant" && events[i].State == "" {
				// A result response is a full message, not a streamed delta.
				if i == 0 || events[i-1].Kind != "status" || events[i-1].Name != "result" {
					events[i].State = "delta"
				}
			}
		}
	}
	// TODO: legacy/shim remove later — old normalized Grok output marks in_progress tool updates as nameless starts; remove after affected artifacts expire or migrate
	if t.OutputDialect == workeroutput.DialectGrokJSON {
		started := map[string]bool{}
		for i, e := range events {
			if e.Kind != "tool" || e.State != "started" || e.ID == "" {
				continue
			}
			if started[e.ID] && e.Name == "" {
				events[i].State = "updated"
			}
			started[e.ID] = true
		}
	}
	items, hidden, _ := fromEventsWithSteering(events, steeringItems(t))
	out.Hidden = hidden

	hasReply := false
	for _, it := range items {
		if it.Kind == Reply {
			hasReply = true
		}
	}
	if t.Done() && !hasReply && t.Result != nil {
		items = append(items, Item{Kind: Reply, TS: t.Ended, Text: resultText(t.Result)})
	}
	if t.Error != "" {
		items = append(items, Item{Kind: Error, TS: t.Ended, Text: t.Error})
	}
	out.Items = append(promptItems(t, promptTS), items...)
	return out
}

func resultText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return "```json\n" + string(b) + "\n```"
}

func steeringItems(t subagent.Turn) []Item {
	var deliveries []subagent.Delivery
	for _, d := range t.Deliveries {
		if d.Kind == "steering" {
			deliveries = append(deliveries, d)
		}
	}
	// A turn woken by posts uses those posts as its prompt, and the runner
	// also records each one as delivered steering. Show such a post once, as
	// the prompt. Those records have no delivery note, so they are dropped
	// before steers are paired with deliveries.
	var steers []subagent.Steer
	for _, s := range t.Steering {
		if s.Message != "" && strings.Contains(t.Prompt, s.Message) {
			continue
		}
		steers = append(steers, s)
	}
	sort.SliceStable(steers, func(i, j int) bool { return steers[i].TS < steers[j].TS })
	var out []Item
	for i, s := range steers {
		from, text := SplitSender(s.Message)
		it := Item{Kind: Steering, anchor: s.SourceOffset, PostID: s.ID, TS: time.UnixMilli(s.TS), From: from, Text: text, Delivered: s.Delivered}
		var d *subagent.Delivery
		if i < len(deliveries) {
			d = &deliveries[i]
		}
		it.Status = DeliveryText(s.Delivered, d, t.Done())
		if s.SourceOffset == nil && (s.Delivered || t.Done()) {
			it.Status += "; source position unavailable"
		}
		out = append(out, it)
	}
	return out
}

// DeliveryText says where a steering message stands.
func DeliveryText(delivered bool, d *subagent.Delivery, turnDone bool) string {
	if !delivered {
		if turnDone {
			return "not delivered (the turn ended first)"
		}
		return "queued · delivered at the worker's next tool boundary"
	}
	if d == nil {
		return "delivered"
	}
	wait := (time.Duration(d.WaitedMs) * time.Millisecond).Round(time.Second)
	switch d.Outcome {
	case "boundary":
		return fmt.Sprintf("delivered at a tool boundary after %s", wait)
	// TODO: legacy/shim remove later — renders steering outcomes from interrupted-tool sessions; remove when retained history has no bound-hit records
	case "bound-hit": // sessions recorded before tool calls stopped being interrupted
		return fmt.Sprintf("delivered after %s: the running tool outlived steering.maxToolWait and was interrupted", wait)
	case "turn-end":
		return "delivered as a continuation when the turn ended"
	case "interrupted":
		if d.Reason == "wait-bound" {
			return fmt.Sprintf("delivered after %s: the running tool outlived steering.maxToolWait and was interrupted", wait)
		}
		if d.Reason != "no-tool-boundaries" {
			return fmt.Sprintf("delivered after %s: the worker was interrupted", wait)
		}
		return fmt.Sprintf("delivered after %s: no tool boundaries in this output format, so the worker was interrupted", wait)
	// TODO: legacy/shim remove later — renders old deliveries until startup migration runs.
	case "unsupported":
		return fmt.Sprintf("delivered after %s: no tool boundaries in this output format, so the worker was interrupted", wait)
	}
	return "delivered"
}

// Activity describes what a running turn is doing now, from its last items.
func Activity(t Turn) string {
	if t.Status == subagent.TurnQueued {
		return "preparing the prompt"
	}
	for i := len(t.Items) - 1; i >= 0; i-- {
		it := t.Items[i]
		switch it.Kind {
		case ToolUse:
			if it.Tool.Done {
				return "thinking"
			}
			target := it.Tool.Target
			if target != "" {
				target = " " + textutil.Clip(target, 60)
			}
			return it.Tool.Verb + target
		case Reply:
			return "writing"
		case Thought:
			return textutil.Clip(it.Text, 60)
		case Compaction:
			if it.Compaction.State == CompactionStarted {
				return "compacting context"
			}
		case Steering:
			if !it.Delivered {
				return "working (steering queued)"
			}
		case Prompt:
			return "starting"
		}
	}
	return "working"
}

// CompactionLine words a compaction for the plain views: "Compacting
// context…", "Context compacted · 122k → 15k tokens · auto", or
// "Context compaction failed: <reason>".
func CompactionLine(c CompactionInfo) string {
	switch c.State {
	case CompactionStarted:
		return "Compacting context…"
	case CompactionFailed:
		if c.Reason != "" {
			return "Context compaction failed: " + c.Reason
		}
		return "Context compaction failed"
	}
	line := "Context compacted"
	var parts []string
	if c.Before > 0 && c.After > 0 {
		parts = append(parts, fmt.Sprintf("%s → %s tokens", CompactTokens(c.Before), CompactTokens(c.After)))
	} else if c.After > 0 {
		parts = append(parts, CompactTokens(c.After)+" tokens after")
	}
	if c.Trigger != "" {
		parts = append(parts, c.Trigger)
	}
	if len(parts) == 0 {
		return line
	}
	return line + " · " + strings.Join(parts, " · ")
}

// CompactTokens words a context size: 999, 122k, 1.1M. The web's
// compactTokens (web/src/lib/sessions/compaction.ts) must print the same
// string for the same event, so both round and both use an upper-case M.
func CompactTokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1000000:
		return strconv.FormatInt(int64(math.Round(float64(n)/1000)), 10) + "k"
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

// ThoughtAside renders "3 thoughts" for folded thoughts ("" for one).
func ThoughtAside(it Item) string {
	if it.Tool.Count > 1 {
		return fmt.Sprintf("%d thoughts", it.Tool.Count)
	}
	return ""
}

// ToolAside renders "(12s, exit 0)" for a tool line ("" when nothing is known).
func ToolAside(t Tool) string {
	var parts []string
	if t.Count > 1 && t.Verb != "edited" && t.Verb != "read" && t.Verb != "wrote" {
		parts = append(parts, fmt.Sprintf("×%d", t.Count))
	}
	if t.Dur >= time.Second {
		parts = append(parts, compactDuration(t.Dur))
	}
	switch {
	case t.Exit != nil:
		parts = append(parts, fmt.Sprintf("exit %d", *t.Exit))
	case t.Failed:
		parts = append(parts, "failed")
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// Headline renders the tool line text without styling: "edited 3 files".
func Headline(t Tool) string {
	if t.Count > 1 {
		noun := "files"
		switch t.Verb {
		case "searched for", "listed":
			noun = "patterns"
		}
		var names []string
		for _, name := range t.Targets {
			if name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return fmt.Sprintf("%s %d %s", t.Verb, t.Count, noun)
		}
		if len(names) > 3 {
			names = append(append([]string(nil), names[:3]...), "…")
		}
		return fmt.Sprintf("%s %d %s (%s)", t.Verb, t.Count, noun, strings.Join(names, ", "))
	}
	return t.Verb
}

func compactDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	d = d.Round(time.Minute)
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
