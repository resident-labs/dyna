package watch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

func workflowEvents(body []byte) (map[string]any, error) {
	var workflow struct {
		On any `yaml:"on"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return nil, err
	}
	result := map[string]any{}
	switch events := workflow.On.(type) {
	case string:
		result[events] = nil
	case []any:
		for _, event := range events {
			name, ok := event.(string)
			if !ok {
				return nil, fmt.Errorf("invalid workflow event")
			}
			result[name] = nil
		}
	case map[string]any:
		return events, nil
	case map[any]any:
		for event, config := range events {
			name, ok := event.(string)
			if !ok {
				return nil, fmt.Errorf("invalid workflow event")
			}
			result[name] = config
		}
	default:
		return nil, fmt.Errorf("missing workflow events")
	}
	return result, nil
}

// Only complete workflow contents from this default-branch commit can disable
// the guard. Unknown data keeps the guard active.
func noDefaultPushCI(raw json.RawMessage, branch string) bool {
	enabled, err := workflowPushEnabled(raw, branch)
	return err == nil && !enabled
}

func workflowPushEnabled(raw json.RawMessage, branch string) (bool, error) {
	if branch == "" {
		return false, fmt.Errorf("workflow contents are unavailable")
	}
	if string(raw) == "null" {
		return false, nil
	}
	var file struct {
		Object *struct {
			Entries []struct {
				Name   string
				Type   string
				Object *struct {
					Text        *string
					IsTruncated bool
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Object == nil || file.Object.Entries == nil {
		return false, fmt.Errorf("workflow contents are unavailable")
	}
	for _, entry := range file.Object.Entries {
		if !strings.HasSuffix(entry.Name, ".yml") && !strings.HasSuffix(entry.Name, ".yaml") {
			continue
		}
		if entry.Type != "blob" || entry.Object == nil || entry.Object.Text == nil || entry.Object.IsTruncated {
			return false, fmt.Errorf("workflow contents are unavailable")
		}
		events, err := workflowEvents([]byte(*entry.Object.Text))
		if err != nil {
			return false, fmt.Errorf("workflow contents are unavailable")
		}
		config, push := events["push"]
		if !push {
			continue
		}
		enabled, err := pushMatchesBranch(config, branch)
		if err != nil {
			return false, err
		}
		if enabled {
			return true, nil
		}
	}
	return false, nil
}

func pushMatchesBranch(config any, branch string) (bool, error) {
	if config == nil {
		return true, nil
	}
	// The YAML decoder produces string keys for workflow mappings.
	encoded, err := json.Marshal(config)
	if err != nil {
		return false, err
	}
	var filters map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &filters); err != nil {
		return false, err
	}
	matches := func(raw json.RawMessage) (bool, error) {
		var patterns []string
		if err := json.Unmarshal(raw, &patterns); err != nil {
			return false, err
		}
		result := false
		for _, pattern := range patterns {
			negative := strings.HasPrefix(pattern, "!")
			pattern = strings.TrimPrefix(pattern, "!")
			matched, err := workflowBranchMatch(pattern, branch)
			if err != nil {
				return false, err
			}
			if matched {
				result = !negative
			}
		}
		return result, nil
	}
	if patterns, ok := filters["branches"]; ok {
		return matches(patterns)
	}
	if patterns, ok := filters["branches-ignore"]; ok {
		ignored, err := matches(patterns)
		return !ignored, err
	}
	// Defining only tag filters excludes all branch pushes.
	_, tags := filters["tags"]
	_, ignoredTags := filters["tags-ignore"]
	return !tags && !ignoredTags, nil
}

// GitHub branch filters use * within one path component and ** across /.
// ? and + repeat the preceding character; bracket ranges and escapes are kept.
func workflowBranchMatch(pattern, branch string) (bool, error) {
	var expression strings.Builder
	expression.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					expression.WriteString("(?:.*/)?")
					i++
				} else {
					expression.WriteString(".*")
				}
				i++
			} else {
				expression.WriteString("[^/]*")
			}
		case '?', '+':
			expression.WriteByte(c)
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				return false, fmt.Errorf("invalid branch range")
			}
			expression.WriteString(pattern[i : i+end+2])
			i += end + 1
		case '\\':
			i++
			if i == len(pattern) {
				return false, fmt.Errorf("invalid branch escape")
			}
			expression.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		default:
			expression.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	expression.WriteString("$")
	return regexp.MatchString(expression.String(), branch)
}
