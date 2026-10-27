package workeroutput

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/textutil"
)

// activityTail bounds how much of a worker's output LastActivity reads.
const activityTail = 16 << 10

// Activity is what a worker is doing now. Since is when its running tool
// call or compaction started (unix ms); it is zero when nothing is running,
// for example after a tool finished.
type Activity struct {
	Text  string `json:"text,omitempty"`
	Since int64  `json:"since,omitempty"`
	// Step is the running tool call's kind, in the closed set the session
	// view words for it (run, wait, read, edit, write, search, fetch, tool):
	// what the call is, not its arguments. It is empty when the worker
	// thinks, writes or compacts, which are not tool steps.
	Step string `json:"step,omitempty"`
	// Steerable says a new message for the session ends this step early: the
	// call is one of Dyna's shell tools, which return on steering (D-349), so
	// the session reads the message at once. Every other call runs to its own
	// end, and the message waits for the next tool boundary.
	Steerable bool `json:"steerable,omitempty"`
}

// The step kinds of a running tool call, as the session view words them.
const (
	StepRun    = "run"
	StepWait   = "wait"
	StepRead   = "read"
	StepEdit   = "edit"
	StepWrite  = "write"
	StepSearch = "search"
	StepFetch  = "fetch"
	StepTool   = "tool"
)

const compactingText = "Compacting context…"

// LastActivity names what a worker is doing now, from the end of its
// normalized output: a running compaction, else the newest tool call (with
// its input's description or first line), else Thinking or Writing. It
// reads only the file's tail, so it stays cheap on every refresh; a torn or
// partial line is skipped. A missing or empty output gives no activity.
func LastActivity(path string) Activity {
	f, err := os.Open(path)
	if err != nil {
		return Activity{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Activity{}
	}
	start := max(info.Size()-activityTail, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return Activity{}
	}
	lines := bytes.Split(buf, []byte("\n"))
	ended := map[string]bool{}
	compactionEnded := false
	// running is the newest tool call that has only progress records so far;
	// its start time is further back.
	var running *Activity
	runningID := ""
	for i := len(lines) - 1; i >= 0; i-- {
		var e Event
		if json.Unmarshal(lines[i], &e) != nil {
			continue
		}
		finished := e.State == "completed" || e.State == "failed"
		switch e.Kind {
		case "status":
			if running != nil || e.Name != "compaction" {
				continue
			}
			if finished {
				compactionEnded = true
			} else if !compactionEnded {
				return Activity{Text: compactingText, Since: e.TS}
			}
		case "tool":
			if finished && e.ID != "" {
				ended[e.ID] = true
			}
			if e.Name == "tool_result" {
				continue
			}
			if running != nil {
				if e.ID == runningID && e.State == "started" {
					running.Since = e.TS
					return *running
				}
				continue
			}
			if !finished && e.State != "started" && e.ID != "" && !ended[e.ID] {
				a := toolActivity(e)
				a.Since = e.TS
				running, runningID = &a, e.ID
				continue
			}
			a := toolActivity(e)
			if e.State == "started" && !ended[e.ID] {
				a.Since = e.TS
			}
			return a
		case "reasoning", "message":
			if running != nil {
				continue
			}
			if e.Kind == "reasoning" {
				return Activity{Text: "Thinking"}
			}
			return Activity{Text: "Writing"}
		}
	}
	if running != nil {
		return *running
	}
	return Activity{}
}

// toolActivity is one tool call as an Activity: its readable line, its step
// kind, and whether a new message for the session ends it early.
func toolActivity(e Event) Activity {
	name := strings.ToLower(strings.TrimSpace(e.Name))
	return Activity{Text: toolLine(e), Step: stepKindOf(name), Steerable: steerableStep(name)}
}

// stepKindOf names what a tool call is, in the closed set the session view
// shares (see Activity.Step): run for a command, wait for a job wait, read,
// edit, write, search and fetch for those tools, and tool for anything else.
// lower is the lowercased tool name.
func stepKindOf(lower string) string {
	bare := bareTool(lower)
	if dynaShellOp(lower) == "output" || bare == "shell_output" {
		return StepWait
	}
	if shellTools[bare] {
		return StepRun
	}
	if kind := stepKinds[bare]; kind != "" {
		return kind
	}
	return StepTool
}

// stepKinds maps the file, search and fetch tools a harness names to the
// session view's step kind for them. A tool that is not here is StepTool.
var stepKinds = map[string]string{
	"read": StepRead, "read_file": StepRead, "view_file": StepRead,
	"edit": StepEdit, "multiedit": StepEdit, "edit_file": StepEdit, "apply_patch": StepEdit, "file_change": StepEdit,
	"write": StepWrite, "write_file": StepWrite,
	"grep": StepSearch, "grep_search": StepSearch, "codebase_search": StepSearch, "search": StepSearch, "web_search": StepSearch, "websearch": StepSearch, "search_web": StepSearch,
	"webfetch": StepFetch, "web_fetch": StepFetch, "read_url_content": StepFetch,
}

// steerableStep reports whether a new message for the session ends this
// running call early: Dyna's own shell run and shell wait return on steering
// (D-349). Another server's tool of the same name, and every other call,
// runs to its own end.
func steerableStep(lower string) bool {
	op := dynaShellOp(lower)
	return op == "run" || op == "output"
}

// dynaShellOp names the job tool of Dyna's MCP server that a tool name calls,
// in each harness's spelling, for example mcp__dyna__shell, dyna_shell or
// dyna.shell. It mirrors transcript.dynaShellOp; the import direction keeps
// the two apart. It returns "run", "output", "stop", or "" for any other
// tool.
func dynaShellOp(lower string) string {
	name := strings.NewReplacer("__", "_", ".", "_", "/", "_", "-", "_", ":", "_").Replace(lower)
	for _, tool := range []struct{ suffix, op string }{{"dyna_shell", "run"}, {"dyna_shell_output", "output"}, {"dyna_shell_stop", "stop"}} {
		if name == tool.suffix || strings.HasSuffix(name, "_"+tool.suffix) {
			return tool.op
		}
	}
	return ""
}

// bareTool strips an MCP server prefix from a tool name, so
// "mcp__dyna__shell" reads "shell". Other spellings pass through.
func bareTool(name string) string {
	name = strings.TrimSpace(name)
	if len(name) < 5 || !strings.EqualFold(name[:5], "mcp__") {
		return name
	}
	rest := name[5:]
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[i+2:]
	}
	return rest
}

// shellTools run a command or read a job's output; the harness's description,
// when it gives one, is already the readable step.
var shellTools = map[string]bool{
	"shell": true, "bash": true, "command": true, "command_execution": true, "run_command": true, "run_terminal_cmd": true,
	"shell_output": true, "shell_jobs": true, "shell_stop": true,
}

// shellJobSteps name the job tools that carry no command of their own.
var shellJobSteps = map[string]string{"shell_output": "reading shell output", "shell_jobs": "reading shell jobs", "shell_stop": "stopping a shell job"}

// fileSteps name what a running file tool does to the file it touches.
var fileSteps = map[string]string{
	"read": "reading", "read_file": "reading", "view_file": "reading",
	"edit": "editing", "multiedit": "editing", "edit_file": "editing", "apply_patch": "editing", "file_change": "editing",
	"write": "writing", "write_file": "writing",
}

// toolLine is a tool call in one line a person can read: the shell's
// description or command, the file a file tool touches, or a name and the one
// argument that says what it works on. A raw tool ID or JSON input never
// becomes the label.
func toolLine(e Event) string {
	name := bareTool(e.Name)
	lower := strings.ToLower(name)
	var input struct {
		Description string `json:"description"`
		Command     string `json:"command"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		Target      string `json:"target_file"`
		URL         string `json:"url"`
		Query       string `json:"query"`
		Pattern     string `json:"pattern"`
	}
	raw := strings.TrimSpace(e.Input)
	// Tool arguments are a JSON object, usually on one line; a command may
	// also arrive as plain text.
	if strings.HasPrefix(raw, "{") {
		if json.Unmarshal([]byte(raw), &input) != nil {
			_ = json.Unmarshal([]byte(textutil.FirstLine(raw)), &input)
		}
	}
	plain := ""
	if !strings.HasPrefix(raw, "{") {
		plain = textutil.FirstLine(raw)
	}
	path := firstText(input.FilePath, input.Path, input.Target)
	if verb, ok := fileSteps[lower]; ok {
		if path != "" {
			return clip(verb + " " + filepath.Base(path))
		}
		return verb + " a file"
	}
	if shellTools[lower] {
		if step := firstText(input.Description, textutil.FirstLine(input.Command), plain); step != "" {
			return clip(step)
		}
		if step, ok := shellJobSteps[lower]; ok {
			return step
		}
		return "running a command"
	}
	step := firstText(input.Description, textutil.FirstLine(input.Command), input.URL, input.Query, input.Pattern)
	if step == "" && path != "" {
		step = filepath.Base(path)
	}
	if step == "" {
		step = plain
	}
	if step == "" {
		return name
	}
	return clip(name + ": " + step)
}

// firstText returns the first value that is not blank, without surrounding
// space.
func firstText(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// labelRunes bounds one activity label.
const labelRunes = 100

// clip bounds a label to labelRunes runes.
func clip(s string) string {
	return textutil.Clip(s, labelRunes)
}
