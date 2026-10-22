package adapters

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// piThinking lists the levels of `pi --thinking` (Pi 1.3.0 --help).
var piThinking = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// modelsPi reads the table that `pi --list-models` prints for the providers
// the account has signed in to:
//
//	provider   model            context  max-out  thinking  images
//	anthropic  claude-fable-5   1M       128K     yes       yes
func modelsPi(ctx context.Context, executable string, env []string) ([]Model, error) {
	out, err := run(ctx, executable, env, "--list-models")
	if err != nil {
		return nil, err
	}
	var models []Model
	seen := map[string]bool{}
	table := false
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[0] == "provider" && f[1] == "model" {
			table = true
			continue
		}
		if !table || len(f) < 5 || f[4] != "yes" && f[4] != "no" {
			continue
		}
		id := f[0] + "/" + f[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		m := Model{ID: id, Name: f[1], Provider: f[0], Context: piTokens(f[2]), Output: piTokens(f[3]), Source: "pi --list-models"}
		if f[4] == "yes" {
			for _, level := range piThinking {
				m.Efforts = append(m.Efforts, Effort{ID: level, Args: []string{"--thinking", level}})
			}
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		return nil, errors.New("Pi model catalog is unavailable")
	}
	return models, nil
}

// piTokens reads sizes such as 200K or 1M; anything else is unknown (0).
func piTokens(s string) int64 {
	scale := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		scale, s = 1000, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		scale, s = 1000000, strings.TrimSuffix(s, "M")
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0
	}
	return int64(n * float64(scale))
}
