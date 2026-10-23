package harness

import (
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/jsonrpc"
)

// codexShellMCPServers names the MCP servers in this launch that serve Dyna's
// shell tools. Dyna generates them from its own settings and passes them in the
// -c mcp_servers override.
func codexShellMCPServers(argv []string) map[string]bool {
	out := map[string]bool{}
	value := ""
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg != "-c" && arg != "--config" {
			continue
		}
		spec := ""
		if key, rest, inline := strings.Cut(arg, "="); inline && (key == "-c" || key == "--config") {
			spec = rest
		} else if i+1 < len(argv) {
			i++
			spec = argv[i]
		}
		if key, val, ok := strings.Cut(spec, "="); ok && key == "mcp_servers" {
			value = val
		}
	}
	if value == "" {
		return out
	}
	for i := strings.IndexByte(value, '{') + 1; i > 0 && i < len(value); {
		switch value[i] {
		case ',', ' ', '\n', '\t', '\r':
			i++
			continue
		}
		eq := strings.IndexByte(value[i:], '=')
		if eq < 0 {
			break
		}
		eq += i
		name := strings.Trim(strings.TrimSpace(value[i:eq]), `"`)
		open := strings.IndexByte(value[eq:], '{')
		if open < 0 {
			break
		}
		open += eq
		depth, end := 0, open
		for ; end < len(value); end++ {
			if value[end] == '{' {
				depth++
			}
			if value[end] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if end > len(value) {
			break
		}
		body := value[open:min(end+1, len(value))]
		if name != "" && strings.Contains(body, `"mcp"`) && strings.Contains(body, `"shell"`) {
			out[name] = true
		}
		i = end + 1
	}
	return out
}

// codexMCPStartupFailure reports a Dyna shell MCP server that did not start.
// The turn would have no Dyna tools, so it fails instead of running blind.
func codexMCPStartupFailure(u jsonrpc.Notification, shellServers map[string]bool) error {
	if u.Method != "mcpServer/startupStatus/updated" || len(shellServers) == 0 {
		return nil
	}
	var p struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(u.Params, &p) != nil || !shellServers[p.Name] {
		return nil
	}
	if p.Status != "failed" && p.Status != "cancelled" {
		return nil
	}
	detail := strings.TrimSpace(p.Error)
	if detail == "" {
		detail = p.Status
	}
	return fmt.Errorf("Dyna shell MCP server %s did not start, so this turn has no Dyna tools: %s", p.Name, detail)
}
