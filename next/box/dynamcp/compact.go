package dynamcp

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// The agy listing. agy declares to the model only the tools whose stored declaration (name, description and parameters) is
// under about 1.8 KB, and the full listing's largest tools are well over it, so agy gets the same tools with a short
// description and bare property schemas: names, types, enums and required stay. The help they drop sits in the server's
// instructions, and a refused call returns the help of its action. tools/call is the same as the full endpoint's.

// surface is what one endpoint lists and says on initialize.
type surface struct {
	tools        []Tool
	instructions string
	// compact adds the action's help to a refused call.
	compact bool
}

func bare(s schema) schema {
	s.Description = ""
	if len(s.Properties) > 0 {
		props := make(map[string]schema, len(s.Properties))
		for key, value := range s.Properties {
			props[key] = bare(value)
		}
		s.Properties = props
	}
	if s.Items != nil {
		item := bare(*s.Items)
		s.Items = &item
	}
	return s
}

func compactTool(t Tool) Tool {
	t.InputSchema = bare(t.InputSchema)
	action := t.InputSchema.Properties["action"]
	action.Enum = append(slices.Clone(action.Enum), agyHelp)
	t.InputSchema.Properties["action"] = action
	t.Description = t.Title + ". Actions: " + strings.Join(action.Enum[:len(action.Enum)-1], ", ") + ". Action help lists fields."
	return t
}

// agyHelp is the action each tool of the agy listing adds: it answers the tool's help, which the listing leaves out, without
// forwarding anything.
const agyHelp = "help"

// helpText is everything the full listing says of a tool: what each action does, then each field with its description, which
// actions take it and its format, from the same registry text.
func helpText(t Tool) string {
	text := t.Description
	for _, key := range slices.Sorted(maps.Keys(t.InputSchema.Properties)) {
		if description := t.InputSchema.Properties[key].Description; description != "" {
			text += "\n" + key + ": " + description
		}
	}
	return text
}

func (s *Server) helpResult(name string) Result {
	tool, _ := s.Tool(name)
	return Result{Content: []Content{{Type: "text", Text: helpText(tool)}}}
}

func (s *Server) agySurface() surface {
	list := make([]Tool, len(s.list))
	help := []string{s.Description() + " Each tool takes action help, which answers its help in full."}
	for i, tool := range s.list {
		list[i] = compactTool(tool)
		help = append(help, "## "+tool.Name+"\n"+helpText(tool))
	}
	return surface{tools: list, instructions: strings.Join(help, "\n\n"), compact: true}
}

// withHelp adds the help of the action a refused call named to its error, so a model that guessed an action or left out a
// field reads what the action takes in place. Refusals that are not about the call itself stay as they were.
func (s *Server) withHelp(out Result, name, verb string) Result {
	var problem contract.Error
	if json.Unmarshal(out.StructuredContent, &problem) != nil || problem.Code != contract.ErrInvalid && problem.Code != contract.ErrUnknownAction {
		return out
	}
	verbs, ok := s.actions[name]
	if !ok {
		return out
	}
	if verb == "" && len(verbs) == 1 {
		verb = slices.Collect(maps.Keys(verbs))[0]
	}
	tool, _ := s.Tool(name)
	a, ok := verbs[verb]
	if !ok {
		problem.Message += "\n" + tool.Description
		return failed(problem)
	}
	problem.Message += "\n" + name + " " + verb + ": " + a.help
	for _, key := range slices.Sorted(maps.Keys(requestProperties(a))) {
		if description := tool.InputSchema.Properties[key].Description; description != "" {
			problem.Message += "\n" + key + ": " + description
		}
	}
	return failed(problem)
}
