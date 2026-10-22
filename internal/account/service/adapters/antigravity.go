package adapters

import (
	"context"
	"strings"
)

func modelsAntigravity(ctx context.Context, executable string, env []string) ([]Model, error) {
	out, err := run(ctx, executable, env, "models")
	if err != nil {
		return nil, err
	}
	var models []Model
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		id, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || id == "" || name == "" || strings.ContainsAny(id, " \t\r\x1b") || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{ID: id, Name: name, Source: "agy models"})
	}
	if len(models) == 0 {
		return nil, ErrUnsupported
	}
	return groupEffortIDs(models), nil
}
