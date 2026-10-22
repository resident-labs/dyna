package adapters

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

func modelsClaude(ctx context.Context, executable string, env []string) ([]Model, error) {
	out, err := run(ctx, executable, env, "--help")
	if err != nil {
		return nil, err
	}
	i := strings.Index(string(out), "--model ")
	if i < 0 {
		return nil, errors.New("Claude model options are unavailable")
	}
	section := string(out)[i:]
	if next := regexp.MustCompile(`\n\s+-{1,2}[a-z]`).FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	efforts := claudeEfforts(string(out))
	var models []Model
	seen := map[string]bool{}
	for _, match := range regexp.MustCompile(`'([a-z][a-z0-9.-]*)'`).FindAllStringSubmatch(section, -1) {
		id := match[1]
		if strings.HasPrefix(id, "claude-") || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{ID: id, Name: id, Provider: "anthropic", Source: "claude --help", Efforts: efforts})
	}
	if len(models) == 0 {
		return nil, errors.New("Claude model options are unavailable")
	}
	return models, nil
}

// claudeEfforts reads the levels that `claude --help` lists for --effort,
// for example "--effort <level>  Effort level ... (low, medium, high)".
// The flag applies to every model, so each model gets the same list.
func claudeEfforts(help string) []Effort {
	m := regexp.MustCompile(`--effort <level>[^(]*\(([a-z, ]+)\)`).FindStringSubmatch(help)
	if m == nil {
		return nil
	}
	var out []Effort
	for _, level := range strings.Split(m[1], ",") {
		if level = strings.TrimSpace(level); level != "" {
			out = append(out, Effort{ID: level, Args: []string{"--effort", level}})
		}
	}
	return out
}
