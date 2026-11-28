// Package dynaext is Dyna's extension of the PiG session, compiled into the box agent. It registers bash and job on
// the box's job server, registers the dyna post and visual tools in place of their MCP ones, and links a nested call to
// the call that made it.
package dynaext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/outside"
	"github.com/resident-labs/dyna/harness/pig"
)

// Config is what the tools reach.
type Config struct {
	Dir        string // where a relative cwd starts: the session's checkout, or a worker's tree
	Shell      *shell.Server
	Dyna       *dynamcp.Server
	Interrupts *Interrupts
}

const (
	// outputLimit is what the model reads of a long output; the whole output goes to a file.
	outputLimit = 12 << 10
	movedNote   = "The job is in the background. You can end your turn and Dyna will wake you when it completes."
	steppedNote = " sent you a message, so this command moved to the background, where it keeps running. job output reads its new output, and Dyna will wake you when it completes."
	// abortedNote is bash's answer when the turn was stopped during the call. The job server keeps a command that had
	// started, in the background, whether it runs in the box or on a host.
	abortedNote = "The turn was stopped while this call ran, so its answer was dropped. A command that had started keeps running in the background; job list shows it, and Dyna will wake you when it completes."
)

// jobSchema is what bash and job give a script. A script reads the whole output here, and the model reads the text.
var jobSchema = json.RawMessage(`{"type":"object","properties":{"output":{"type":"string"},"job":{"type":"string"},"running":{"type":"boolean"},"status":{"type":"string"},"exit_code":{"type":"number"},"message_arrived":{"type":"boolean"},"interrupt":{"type":"object","properties":{"kind":{"type":"string"},"name":{"type":"string"}}},"jobs":{"type":"array","items":{}}},"additionalProperties":false}`)

// resultSchema is the result schema PiG gives every MCP tool, so a script sees one shape.
var resultSchema = json.RawMessage(`{"type":"object","properties":{"content":{"type":"array","items":{}},"structuredContent":{},"isError":{"type":"boolean"}},"required":["content"],"additionalProperties":true}`)

// Extension returns the extension. It fails to load when the dyna server declares no post or visual tool.
func Extension(c Config) extension.InlineExtension {
	return extension.NamedInlineExtension{Name: "dyna", Factory: func(api extension.API) error {
		post, hasPost := c.Dyna.Tool("post")
		visual, hasVisual := c.Dyna.Tool("visual")
		if !hasPost || !hasVisual {
			return errors.New("the dyna server declares no post or visual tool")
		}
		// roots maps each call of the turn to the call the model made, which a nested call's post is linked to.
		roots := map[string]string{}
		// used holds the tools called since the session started or last compacted, nested calls of a script included.
		var usedMu sync.Mutex
		used := map[string]bool{}
		api.OnContext(imageContext())
		reader := &shell.OutputReader{}
		api.OnTurnStart(func(context.Context, extension.TurnStartEvent) error { clear(roots); return nil })
		// PiG's turn_start is each model request; agent_start owns the whole input run.
		api.OnAgentStart(func(context.Context, extension.AgentStartEvent) error {
			reader.Reset()
			return nil
		})
		api.OnToolCall(func(_ context.Context, e extension.ToolCallEvent) (extension.ToolCallEventResult, error) {
			call := e.(extension.CustomToolCallEvent)
			root := call.ToolCallID
			if call.ParentToolCallID != "" {
				root = call.ParentToolCallID
				if outer, ok := roots[call.ParentToolCallID]; ok {
					root = outer
				}
			}
			roots[call.ToolCallID] = root
			usedMu.Lock()
			used[call.ToolName] = true
			usedMu.Unlock()
			if strings.HasPrefix(call.ToolName, "mcp__dyna__") {
				call.Input[contract.ToolCallIDArgument] = root
			}
			return extension.ToolCallEventResult{}, nil
		})
		// A compaction unloads each tool tool_search could have loaded (exposure deferred or codemode) that nothing called
		// since the last one; a search loads it again. PiG's compaction head then declares the tools left (dyna patch P11).
		api.OnSessionBeforeCompact(func(context.Context, extension.SessionBeforeCompactEvent) (extension.SessionBeforeCompactResult, error) {
			usedMu.Lock()
			defer usedMu.Unlock()
			searchable := map[string]bool{}
			for _, tool := range api.GetAllTools() {
				searchable[tool.Name] = tool.Exposure == extension.ToolExposureDeferred || tool.Exposure == extension.ToolExposureCodemode
			}
			active := api.GetActiveTools()
			kept := slices.DeleteFunc(slices.Clone(active), func(name string) bool { return searchable[name] && !used[name] })
			api.SetActiveTools(kept)
			clear(used)
			return extension.SessionBeforeCompactResult{}, nil
		})

		// What a server other than dyna returns is outside text: github's as itself, any other by its name. Its author is each
		// item's own and unknown here.
		api.OnToolResult(outsideResult)
		postDefinition, err := postTool(c, post, roots)
		if err != nil {
			return err
		}
		visualDefinition, err := visualTool(c, visual)
		if err != nil {
			return err
		}
		interrupts := c.Interrupts
		if interrupts == nil {
			interrupts = NewInterrupts()
		}
		api.RegisterTool(codemodeTool(interrupts, api.GetSettings))
		api.RegisterTool(bash(c, roots))
		api.RegisterTool(job(c, reader))
		api.RegisterTool(postDefinition)
		api.RegisterTool(visualDefinition)
		return nil
	}}
}

func bash(c Config, roots map[string]string) extension.ToolDefinition {
	return extension.ToolDefinition{
		Name:          "bash",
		Label:         "bash",
		Description:   "Run bash here or on a granted host. After 30 s or a message from the owner or an ancestor, the command continues as a job. A host job keeps running through a box refresh or harness switch, but its job handle is gone; the engine reports its result. Don't detach it (nohup, setsid, disown or trailing &): Dyna loses it. Sleep-only commands, even followed by echo or true, are refused. bash -c puts the command in the process list; pkill -f can kill that shell too.\nOutput shows up to the last 12 KiB and names a file with the full output when cut. Narrow searches (rg -l for paths, -n for lines, -c for counts) or page with read. After a failed commit hook, fix, stage and retry; don't amend. Never bypass hooks or signing without explicit owner approval.",
		PromptSnippet: "Run bash commands, here or on a host",
		Parameters:    json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string","description":"What the command is for, as its title in the Jobs list"},"cwd":{"type":"string"},"host":{"type":"string"},"timeout":{"type":"integer","description":"Seconds, not milliseconds, before the command is stopped, at most 7200. A host run stops at 600 by default"}},"required":["command"]}`),
		OutputSchema:  jobSchema,
		Execute: func(ctx context.Context, id string, params json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			var run shell.Run
			if err := json.Unmarshal(params, &run); err != nil {
				return extension.AgentToolResult{}, err
			}
			run.CallID = roots[id]
			// A host starts from its login folder; in the box a relative cwd starts from the session's or worker's tree.
			if run.Host == "" && !filepath.IsAbs(run.Cwd) {
				run.Cwd = filepath.Join(c.Dir, run.Cwd)
			}
			answer, err := c.Shell.Run(ctx, run)
			if err != nil && ctx.Err() != nil {
				return extension.AgentToolResult{}, errors.New(abortedNote)
			}
			if err != nil {
				return extension.AgentToolResult{}, err
			}
			return jobResult(answer, true)
		},
	}
}

func job(c Config, reader *shell.OutputReader) extension.ToolDefinition {
	return extension.ToolDefinition{
		Name:          "job",
		Label:         "job",
		Description:   "Dyna wakes you when a job ends, at 5, 15 and 30 minutes then hourly, and once after 5 minutes with no output or activity. Check output before judging progress.\noutput: new text since the last read (up to 12 KiB; full output saved to a file). Waits up to 30 s, or until completion or a message. After a still-running result, another check that turn doesn't wait or consume output; it works normally next turn or when finished.\nstop: stop the job's process group.",
		PromptSnippet: "Read, stop or list background jobs",
		Parameters:    json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["output","stop","list"]},"job":{"type":"string"}},"required":["action"]}`),
		OutputSchema:  jobSchema,
		Execute: func(_ context.Context, _ string, params json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			var call shell.JobCall
			if err := json.Unmarshal(params, &call); err != nil {
				return extension.AgentToolResult{}, err
			}
			answer, err := reader.Job(c.Shell, call)
			if err != nil {
				return extension.AgentToolResult{}, err
			}
			if result, ok := answer.(shell.Result); ok {
				return jobResult(result, false)
			}
			list := answer.(shell.Jobs)
			lines := make([]string, len(list.Jobs))
			for i, row := range list.Jobs {
				command := row.Command
				if row.Host != "" {
					command = "on " + string(row.Host) + ": " + command
				}
				lines[i] = string(row.Job) + "\t" + row.Status + "\t" + command
			}
			return textResult(strings.Join(lines, "\n"), list)
		},
	}
}

// jobResult is one job's state. A nonzero exit is not a tool error, since grep finding nothing is one: the text says the code.
func jobResult(answer shell.Result, moved bool) (extension.AgentToolResult, error) {
	state := fmt.Sprintf("[job %s: %s]", answer.Job, answer.Status)
	switch {
	case answer.Repeated:
		state = fmt.Sprintf("[job %s is still running; you already checked it this turn. End your turn: Dyna wakes you when it ends.]", answer.Job)
		answer.Output = state
		return textResult(state, answer)
	case answer.Running && answer.MessageArrived:
		state = fmt.Sprintf("[job %s] %s%s", answer.Job, answer.Interrupt.Sender(), steppedNote)
	case answer.Running && moved:
		state = fmt.Sprintf("[job %s] %s", answer.Job, movedNote)
	case answer.Running:
		state = fmt.Sprintf("[job %s is still running. Do not check it again: Dyna wakes you with its result when it ends. Do other work now, or end your turn.]", answer.Job)
	}
	return textResult(strings.TrimLeft(strings.TrimRight(answer.Output, " \t\r\n")+"\n"+state, " \t\r\n"), answer)
}

// textResult gives the model the text, cut to its end when long, with the rest in a file, and a script the whole answer.
func textResult(text string, answer any) (extension.AgentToolResult, error) {
	structured, err := json.Marshal(answer)
	if err != nil {
		return extension.AgentToolResult{}, err
	}
	if len(text) > outputLimit {
		limit := outputLimit
		cut := sdk.TruncateTail(text, sdk.TruncationOptions{MaxBytes: &limit})
		name := make([]byte, 16)
		_, _ = rand.Read(name)
		path := filepath.Join(os.TempDir(), "dyna-output-"+hex.EncodeToString(name)+".txt")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			return extension.AgentToolResult{}, err
		}
		text = fmt.Sprintf("%s\n\n[Output cut to the last %s of %s. Full output: %s (read it with offset/limit)]", cut.Content, sdk.FormatSize(cut.OutputBytes), sdk.FormatSize(cut.TotalBytes), path)
	}
	return extension.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: text}}, StructuredContent: structured}, nil
}

// declared is a dyna tool under the name and namespace PiG's MCP extension gives it, so it takes that tool's place.
func declared(tool dynamcp.Tool) (extension.ToolDefinition, error) {
	parameters, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return extension.ToolDefinition{}, err
	}
	a := tool.Annotations
	return extension.ToolDefinition{
		Name:         "mcp__dyna__" + tool.Name,
		Label:        "dyna/" + tool.Name,
		Description:  tool.Description,
		Parameters:   parameters,
		OutputSchema: resultSchema,
		Namespace:    &extension.ToolNamespace{Name: "mcp__dyna", Aliases: []string{"dyna"}},
		Annotations:  &extension.ToolAnnotations{ReadOnlyHint: &a.ReadOnly, DestructiveHint: &a.Destructive, IdempotentHint: &a.Idempotent, OpenWorldHint: &a.OpenWorld},
	}, nil
}

// postTool takes the place of the MCP post tool, because only a tool's own result can end the turn.
func postTool(c Config, tool dynamcp.Tool, roots map[string]string) (extension.ToolDefinition, error) {
	definition, err := declared(tool)
	definition.Exposure = extension.ToolExposureDirect
	definition.Execute = func(ctx context.Context, id string, params json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(params, &args); err != nil {
			return extension.AgentToolResult{}, err
		}
		result := c.Dyna.Call(ctx, "post", args)
		var endTurn bool
		_ = json.Unmarshal(args["endTurn"], &endTurn)
		asked := endTurn && !result.IsError
		// A script's nested result never reaches the agent loop, so only a direct call can end the turn.
		direct := roots[id] == id
		if asked && !direct {
			var fields map[string]any
			if err := json.Unmarshal(result.StructuredContent, &fields); err != nil {
				return extension.AgentToolResult{}, err
			}
			fields["turnContinues"] = true
			result.StructuredContent, _ = json.Marshal(fields)
			result.Content = []dynamcp.Content{{Type: "text", Text: string(result.StructuredContent)}}
		}
		var posted struct {
			Event contract.EventID `json:"event"`
		}
		_ = json.Unmarshal(result.StructuredContent, &posted)
		out, err := mcpResult(result, details{Server: "dyna", Tool: "post"})
		// The turn ends only with a post the harness can report as its reply.
		if asked && direct && posted.Event != "" {
			out.Details, out.Terminate = details{Server: "dyna", Tool: "post", EndPost: pig.EndPost{EndPost: posted.Event}}, true
		}
		return out, err
	}
	return definition, err
}

// visualTool runs the visual check in order: the calls of one message run in parallel unless one is sequential, and a
// check reads a file that a write before it in the same message may not have finished. It waits for tool_search.
func visualTool(c Config, tool dynamcp.Tool) (extension.ToolDefinition, error) {
	definition, err := declared(tool)
	definition.Exposure = extension.ToolExposureDeferred
	definition.ExecutionMode = "sequential"
	definition.Execute = func(ctx context.Context, _ string, params json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(params, &args); err != nil {
			return extension.AgentToolResult{}, err
		}
		return mcpResult(c.Dyna.Call(ctx, "visual", args), details{Server: "dyna", Tool: "visual"})
	}
	return definition, err
}

// details are what the tool records on its result: the MCP server and tool, as PiG's MCP extension records them, and
// the post that ended the turn.
type details struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
	pig.EndPost
}

// mcpResult is an MCP result as PiG's MCP extension gives one: its content, and the whole result for a script.
func mcpResult(result dynamcp.Result, d details) (extension.AgentToolResult, error) {
	structured, err := json.Marshal(result)
	out := extension.AgentToolResult{Details: d, StructuredContent: structured, IsError: result.IsError}
	for _, block := range result.Content {
		switch block.Type {
		case "image":
			out.Content = append(out.Content, ai.ImageContent{Data: block.Data, MimeType: block.MimeType})
		default:
			out.Content = append(out.Content, ai.TextContent{Text: block.Text})
		}
	}
	return out, err
}

// outsideResult wraps the text of a tool result from a server other than dyna: github's as itself, any other by its name.
// A server name has no "__" (checkMCP), so the first one ends it. Images, the error flag and the structured content stay as
// they are. It wraps a model's direct calls only: a codemode script reads the raw result of its own calls (spec decision 13).
func outsideResult(_ context.Context, e extension.ToolResultEvent) (extension.ToolResultEventResult, error) {
	result := e.(extension.CustomToolResultEvent)
	server, _, found := strings.Cut(strings.TrimPrefix(result.ToolName, "mcp__"), "__")
	if !found || !strings.HasPrefix(result.ToolName, "mcp__") || server == "dyna" {
		return extension.ToolResultEventResult{}, nil
	}
	origin := outside.MCP(server)
	content := make([]any, len(result.Content))
	for i, block := range result.Content {
		content[i] = block
		// PiG gives a block as the JSON object it is.
		if fields, ok := block.(map[string]any); ok && fields["type"] == "text" {
			text, _ := fields["text"].(string)
			content[i] = map[string]any{"type": "text", "text": outside.Wrap(origin, text)}
		}
	}
	return extension.ToolResultEventResult{Content: content, StructuredContent: result.StructuredContent}, nil
}
