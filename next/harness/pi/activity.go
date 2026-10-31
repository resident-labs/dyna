package pi

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func activityLabel(e event) string {
	label := e.ToolName
	for _, key := range []string{"path", "command", "pattern", "query", "url"} {
		var value string
		if json.Unmarshal(e.Args[key], &value) != nil || value == "" {
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
	runes := []rune(label)
	if len(runes) > 80 {
		label = string(runes[:79]) + "…"
	}
	return label
}
