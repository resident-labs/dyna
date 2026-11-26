package githubmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The agy listing. agy declares to the model only the tools whose stored declaration (name, description and parameters) is under
// about 1.8 KB, and several of GitHub's are well over it. agy's server lists the same tools with the same names, types, enums and
// required fields, the first line of each description and bare properties; github_help answers the full text of one. tools/call is
// the same handler as the full endpoint's. The cap here is below agy's cut, so a tool the writes setting adds fits too.

// helpName is the one tool the agy listing adds. It forwards nothing.
const helpName = "github_help"

const (
	firstLine = 160
	pointer   = " Call " + helpName + " for fields."
	// maxTool is the most any tool of the agy listing may declare, in bytes of name, description and parameters as JSON.
	maxTool = 1500
)

func compactDescription(full string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(full), "\n")
	if runes := []rune(line); len(runes) > firstLine {
		line = string(runes[:firstLine-1]) + "…"
	}
	return line + pointer
}

// bare drops every description of a schema's properties, at any depth, and reduces the objects an array holds to {"type":"object"}:
// their shapes (projects_write's items and iterations) are most of such a tool's size, and github_help gives them whole. "properties" holds field names, which may be
// "description", so its keys are never taken for the keyword.
func bare(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			switch {
			case key == "description":
			case key == "items" && holdsObjects(value):
				out[key] = map[string]any{"type": "object"}
			case key == "properties":
				fields, _ := value.(map[string]any)
				kept := make(map[string]any, len(fields))
				for name, field := range fields {
					kept[name] = bare(field)
				}
				out[key] = kept
			default:
				out[key] = bare(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = bare(value)
		}
		return out
	}
	return v
}

// terse is for a tool still over maxTool once bare: a property keeps its name and type, and its enum values and nested fields, which
// github_help lists, go. projects_write is the one that needs it.
func terse(schema any) any {
	object, _ := schema.(map[string]any)
	properties, _ := object["properties"].(map[string]any)
	for name, value := range properties {
		field, _ := value.(map[string]any)
		delete(field, "enum")
		if _, nested := field["properties"]; nested {
			properties[name] = map[string]any{"type": "object"}
		}
	}
	return schema
}

func holdsObjects(items any) bool {
	object, _ := items.(map[string]any)
	_, shaped := object["properties"]
	_, oneOf := object["oneOf"]
	return shaped || oneOf
}

// fields lists every property of a schema with its type, enum and description, nested ones by their dotted path.
func fields(prefix string, schema map[string]any, out *[]string) {
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range properties {
			if !yield(name) {
				return
			}
		}
	}) {
		field, _ := properties[name].(map[string]any)
		line := prefix + name
		if kind, ok := field["type"].(string); ok {
			line += " (" + kind
			if enum, ok := field["enum"].([]any); ok {
				line += ", one of " + strings.Trim(fmt.Sprint(enum), "[]")
			}
			line += ")"
		}
		if description, ok := field["description"].(string); ok {
			line += ": " + description
		}
		*out = append(*out, line)
		fields(prefix+name+".", field, out)
		if items, ok := field["items"]; ok && holdsObjects(items) {
			shape, _ := json.Marshal(items)
			*out = append(*out, prefix+name+"[] each is: "+string(shape))
		}
	}
}

// helpText is everything the full listing says of a tool: its whole description and each field.
func helpText(name, description string, schema any) (string, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	lines := []string{name + ": " + description}
	fields("", parsed, &lines)
	return strings.Join(lines, "\n"), nil
}

// compactTool replaces a tool's description and schema with the agy listing's, and returns the help it drops.
func compactTool(tool *mcp.Tool) (string, error) {
	help, err := helpText(tool.Name, tool.Description, tool.InputSchema)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(tool.InputSchema)
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	tool.Description = compactDescription(tool.Description)
	tool.InputSchema = bare(parsed)
	if size, _ := json.Marshal(map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema}); len(size) > maxTool {
		tool.InputSchema = terse(tool.InputSchema)
	}
	return help, nil
}

// helpTool is github_help over the help of the tools this server lists.
func helpTool(help map[string]string) (*mcp.Tool, mcp.ToolHandler) {
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range help {
			if !yield(name) {
				return
			}
		}
	})
	tool := &mcp.Tool{
		Name:        helpName,
		Description: "The full description and every field of one GitHub tool; no tool lists them all.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"tool": map[string]any{"type": "string"}}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}
	return tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "tool must be a string"}}}, nil
		}
		text, ok := help[args.Tool]
		switch {
		case args.Tool == "":
			var lines []string
			for _, name := range names {
				line, _, _ := strings.Cut(strings.TrimPrefix(help[name], name+": "), "\n")
				lines = append(lines, name+": "+line)
			}
			text = strings.Join(lines, "\n")
		case !ok:
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no tool " + args.Tool + "; the tools are " + strings.Join(names, ", ")}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}

// withHelp adds a tool's help to a refused call (arguments the tool does not take), so a model that guessed a field reads what the
// tool takes in place. A failure from GitHub itself stays as it was.
func withHelp(help map[string]string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil || help[call.Params.Name] == "" {
				return result, err
			}
			if err != nil && strings.Contains(err.Error(), "validating") {
				return result, fmt.Errorf("%w\n%s", err, help[call.Params.Name])
			}
			if out, ok := result.(*mcp.CallToolResult); ok && err == nil && out.IsError && len(out.Content) == 1 {
				if text, ok := out.Content[0].(*mcp.TextContent); ok && strings.HasPrefix(text.Text, "invalid arguments") {
					copied := *out
					copied.Content = []mcp.Content{&mcp.TextContent{Text: text.Text + "\n" + help[call.Params.Name]}}
					return &copied, nil
				}
			}
			return result, err
		}
	}
}
