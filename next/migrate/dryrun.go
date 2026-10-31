package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Report contains counts derived from decoded records, not declared counts.
type Report struct {
	Global                contract.BundleCounts                       `json:"global"`
	Profiles              int                                         `json:"profiles"`
	ModelsCheckedAtImport []string                                    `json:"modelsCheckedAtImport"`
	Repositories          int                                         `json:"repositories"`
	Skills                int                                         `json:"skills"`
	Rooms                 map[contract.RoomName]contract.BundleCounts `json:"rooms"`
}

// DryRun validates every part without opening or writing a store.
func DryRun(dir string) (Report, error) {
	var report Report
	f, err := os.Open(filepath.Join(dir, "global.json"))
	if err != nil {
		return report, err
	}
	global, err := contract.DecodeBundle[contract.GlobalBundle](f)
	f.Close()
	if err != nil {
		return report, fmt.Errorf("global: %w", err)
	}
	report.Profiles, report.Repositories, report.Skills = len(global.Profiles), len(global.Repos), len(global.Skills)
	models := make(map[string]bool)
	for _, profile := range global.Profiles {
		if profile.Model != "" {
			models[profile.Model] = true
		}
		for _, model := range profile.Models {
			models[model] = true
		}
	}
	for model := range models {
		report.ModelsCheckedAtImport = append(report.ModelsCheckedAtImport, model)
	}
	slices.Sort(report.ModelsCheckedAtImport)
	report.Global = contract.BundleCounts{
		Hosts: len(global.Hosts), Accounts: len(global.Accounts),
		Devices: len(global.Devices), Subscriptions: len(global.Subscriptions),
	}
	if err := compareCounts(report.Global, global.Counts); err != nil {
		return report, fmt.Errorf("global: %w", err)
	}
	if global.Rooms == nil {
		return report, fmt.Errorf("global: missing room manifest")
	}
	names := make(map[string]bool, len(global.Rooms))
	for _, name := range global.Rooms {
		s := string(name)
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") || strings.IndexByte(s, 0) >= 0 {
			return report, fmt.Errorf("invalid room name")
		}
		if names[s] {
			return report, fmt.Errorf("duplicate room name")
		}
		names[s] = true
	}
	entries, err := os.ReadDir(filepath.Join(dir, "rooms"))
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".json") || !names[strings.TrimSuffix(name, ".json")] {
			return report, fmt.Errorf("unexpected room file")
		}
	}
	report.Rooms = make(map[contract.RoomName]contract.BundleCounts, len(names))
	for _, name := range global.Rooms {
		f, err := os.Open(filepath.Join(dir, "rooms", string(name)+".json"))
		if err != nil {
			return report, err
		}
		room, err := contract.DecodeBundle[contract.RoomBundle](f)
		f.Close()
		if err != nil {
			return report, fmt.Errorf("room %s: %w", name, err)
		}
		if room.Name != name {
			return report, fmt.Errorf("room %s: name mismatch", name)
		}
		counts := contract.BundleCounts{
			Goals: len(room.Goals), Issues: len(room.Issues),
			PRs: len(room.PRs), Questions: len(room.Questions),
			Subscriptions: len(room.Subscriptions), Sessions: len(room.Handoff),
		}
		for _, goal := range room.Goals {
			counts.GoalItems += len(goal.Items)
		}
		if err := compareCounts(counts, room.Counts); err != nil {
			return report, fmt.Errorf("room %s: %w", name, err)
		}
		report.Rooms[name] = counts
	}
	return report, nil
}

func compareCounts(actual, declared contract.BundleCounts) error {
	for _, c := range []struct {
		name             string
		actual, declared int
	}{
		{"goals", actual.Goals, declared.Goals},
		{"goalItems", actual.GoalItems, declared.GoalItems}, {"issues", actual.Issues, declared.Issues},
		{"prs", actual.PRs, declared.PRs}, {"questions", actual.Questions, declared.Questions},
		{"subscriptions", actual.Subscriptions, declared.Subscriptions}, {"hosts", actual.Hosts, declared.Hosts},
		{"accounts", actual.Accounts, declared.Accounts}, {"devices", actual.Devices, declared.Devices},
		{"sessions", actual.Sessions, declared.Sessions},
	} {
		if c.actual != c.declared {
			return fmt.Errorf("%s count: records %d, declared %d", c.name, c.actual, c.declared)
		}
	}
	return nil
}
