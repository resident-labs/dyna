package pi

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func activityLabel(e event) string {
	label := e.ToolName
	var args map[string]json.RawMessage
	_ = json.Unmarshal(e.Args, &args)
	var host string
	if e.ToolName == "bash" && json.Unmarshal(args["host"], &host) == nil && host != "" {
		label += " on " + host + ":"
	}
	for _, key := range []string{"path", "command", "pattern", "query", "url", "job"} {
		var value string
		if json.Unmarshal(args[key], &value) != nil || value == "" {
			continue
		}
		if key == "path" {
			value = filepath.Base(value)
		}
		label += " " + value
		break
	}
	label = strings.SplitN(label, "\n", 2)[0]
	label = strings.ReplaceAll(label, "\r", " ")
	return label
}
