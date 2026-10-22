package adapters

import (
	"regexp"
	"slices"
	"strings"
)

// effortLevels is the order the picker shows. Cursor and Antigravity encode
// one of these as a model ID segment, for example claude-fable-5-thinking-high
// or gemini-4-argon-low.
var effortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

var effortSegment = regexp.MustCompile(`-(none|minimal|low|medium|high|xhigh|max)(-|$)`)

// effortWord matches the level in a display name: "Extra High", "(Low)".
var effortWord = regexp.MustCompile(`(?i)\s*\(?\b(extra high|xhigh|minimal|medium|high|low|max|none)\b\)?`)

// groupEffortIDs collapses models whose IDs differ only by one effort segment
// into one model with efforts that select each ID. The group keeps the
// position of its first member. Its ID is the member whose name names no
// level (the harness default), else the medium member, else the first one.
func groupEffortIDs(models []Model) []Model {
	key := func(id string) (string, string) {
		loc := effortSegment.FindAllStringSubmatchIndex(id, -1)
		if loc == nil {
			return "", ""
		}
		last := loc[len(loc)-1]
		return id[:last[0]] + id[last[4]:], id[last[2]:last[3]]
	}
	members := map[string][]int{}
	levels := map[string]map[string]bool{}
	for i, m := range models {
		if k, level := key(m.ID); k != "" {
			members[k] = append(members[k], i)
			if levels[k] == nil {
				levels[k] = map[string]bool{}
			}
			if levels[k][level] {
				levels[k][""] = true // two IDs with one level: keep them apart
			}
			levels[k][level] = true
		}
	}
	var out []Model
	done := map[string]bool{}
	for _, m := range models {
		k, _ := key(m.ID)
		group := members[k]
		if k == "" || len(group) < 2 || levels[k][""] {
			out = append(out, m)
			continue
		}
		if done[k] {
			continue
		}
		done[k] = true
		lead := -1
		for _, i := range group {
			if !effortWord.MatchString(models[i].Name) {
				lead = i
			}
		}
		for _, i := range group {
			if _, level := key(models[i].ID); lead < 0 && level == "medium" {
				lead = i
			}
		}
		if lead < 0 {
			lead = group[0]
		}
		g := models[lead]
		g.Name = strings.TrimSpace(effortWord.ReplaceAllString(g.Name, ""))
		g.Efforts = nil
		for _, i := range group {
			_, level := key(models[i].ID)
			g.Efforts = append(g.Efforts, Effort{ID: level, Default: i == lead, Model: models[i].ID})
		}
		slices.SortStableFunc(g.Efforts, func(a, b Effort) int {
			return slices.Index(effortLevels, a.ID) - slices.Index(effortLevels, b.ID)
		})
		out = append(out, g)
	}
	return out
}
