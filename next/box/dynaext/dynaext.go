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
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
)

// Config is what the tools reach.
type Config struct {
	Dir   string // where a relative cwd starts: the session's checkout, or a worker's tree
	Shell *shell.Server
	Dyna  *dynamcp.Server
}

const (
	// searchLimit is how many tools a search loads unless the model chose a number. Each loaded tool stays declared
	// for the rest of the session, and PiG's default of 8 is most of the dyna tools.
	searchLimit = 3
	// outputLimit is what the model reads of a long output; the whole output goes to a file.
	outputLimit = 20 << 10
	movedNote   = "The job is in the background. You can end your turn and Dyna will wake you when it completes."
	steppedNote = "The owner sent you a message, so this command moved to the background, where it keeps running. Read the message now. job output reads its new output, and Dyna will wake you when it completes."
	// abortedNote is bash's answer when the turn was stopped during the call. The job server keeps a command that had
	// started, in the background, whether it runs in the box or on a host.
	abortedNote = "The turn was stopped while this call ran, so its answer was dropped. A command that had started keeps running in the background; job list shows it, and Dyna will wake you when it completes."
)

// jobSchema is what bash and job give a script. A script reads the whole output here, and the model reads the text.
var jobSchema = json.RawMessage(`{"type":"object","properties":{"output":{"type":"string"},"job":{"type":"string"},"running":{"type":"boolean"},"status":{"type":"string"},"exit_code":{"type":"number"},"message_arrived":{"type":"boolean"},"jobs":{"type":"array","items":{}}},"additionalProperties":false}`)

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
		api.OnContext(imageContext())
		api.OnTurnStart(func(context.Context, extension.TurnStartEvent) error { clear(roots); return nil })
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
			switch {
			case strings.HasPrefix(call.ToolName, "mcp__dyna__"):
				call.Input[contract.ToolCallIDArgument] = root
			case call.ToolName == "tool_search" && call.Input["limit"] == nil:
				call.Input["limit"] = searchLimit
			}
			return extension.ToolCallEventResult{}, nil
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
		api.RegisterTool(bash(c, roots))
		api.RegisterTool(job(c))
		api.RegisterTool(postDefinition)
		api.RegisterTool(visualDefinition)
		return nil
	}}
}

func bash(c Config, roots map[string]string) extension.ToolDefinition {
	return extension.ToolDefinition{
		Name:          "bash",
		Label:         "bash",
		Description:   "Run a bash command. It runs in the foreground for up to 30 s, then keeps running in the background as a job.\njob output returns only text printed since the last read, and is empty when a job printed nothing.\nThe command runs as bash -c, so its text is in the process list. pkill -f with a pattern from that text also matches this shell and ends the job with SIGTERM.\nhost runs the command on that machine instead of this box, under your host grant.",
		PromptSnippet: "Run bash commands, here or on a host",
		Parameters:    json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string","description":"What the command is for, as its title in the Jobs list"},"cwd":{"type":"string"},"host":{"type":"string"},"timeout":{"type":"integer","description":"Seconds before the command is stopped, at most 7200. A host run stops at 600 by default"}},"required":["command"]}`),
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

func job(c Config) extension.ToolDefinition {
	return extension.ToolDefinition{
		Name:          "job",
		Label:         "job",
		Description:   "Background jobs that bash started.\noutput: read a job's new output.\nstop: stop a job and its whole process group.\nlist: list the jobs.",
		PromptSnippet: "Read, stop or list background jobs",
		Parameters:    json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["output","stop","list"]},"job":{"type":"string"}},"required":["action"]}`),
		OutputSchema:  jobSchema,
		Execute: func(_ context.Context, _ string, params json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			var call shell.JobCall
			if err := json.Unmarshal(params, &call); err != nil {
				return extension.AgentToolResult{}, err
			}
			answer, err := c.Shell.Job(call)
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
	case answer.Running && moved && answer.MessageArrived:
		state = fmt.Sprintf("[job %s] %s", answer.Job, steppedNote)
	case answer.Running && moved:
		state = fmt.Sprintf("[job %s] %s", answer.Job, movedNote)
	case answer.Running:
		state = fmt.Sprintf("[job %s is running in the background]", answer.Job)
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
	source := "mcp " + server
	if server == "github" {
		source = server
	}
	content := make([]any, len(result.Content))
	for i, block := range result.Content {
		content[i] = block
		// PiG gives a block as the JSON object it is.
		if fields, ok := block.(map[string]any); ok && fields["type"] == "text" {
			text, _ := fields["text"].(string)
			content[i] = map[string]any{"type": "text", "text": contract.Outside(source, "", text)}
		}
	}
	return extension.ToolResultEventResult{Content: content, StructuredContent: result.StructuredContent}, nil
}
