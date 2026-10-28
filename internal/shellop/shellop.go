// Package shellop classifies the tool names that operate on Dyna shell jobs.
package shellop

import "strings"

// Operation is a Dyna shell job operation. The zero value means the tool is
// not one of Dyna's shell job tools.
type Operation string

const (
	Run    Operation = "run"
	Output Operation = "output"
	Stop   Operation = "stop"
)

var toolNameSeparators = strings.NewReplacer("__", "_", ".", "_", "/", "_", "-", "_", ":", "_")

// Classify names the job tool of Dyna's MCP server that a tool name calls, in
// each harness's spelling, for example mcp__dyna__shell, dyna_shell or
// dyna.shell. lower is the lowercased tool name.
func Classify(lower string) Operation {
	name := toolNameSeparators.Replace(lower)
	for _, tool := range []struct {
		suffix string
		op     Operation
	}{
		{"dyna_shell", Run},
		{"dyna_shell_output", Output},
		{"dyna_shell_stop", Stop},
	} {
		if name == tool.suffix || strings.HasSuffix(name, "_"+tool.suffix) {
			return tool.op
		}
	}
	return ""
}
