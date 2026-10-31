package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/settings"
	"github.com/resident-labs/dyna/contract"
)

func readSettings(configDir string) (contract.SettingsBundle, error) {
	path := filepath.Join(configDir, "settings.json")
	if _, err := os.Stat(path); err != nil {
		return contract.SettingsBundle{}, fmt.Errorf("captured settings: %w", err)
	}
	v, err := settings.Load(path)
	if err != nil {
		return contract.SettingsBundle{}, err
	}
	return contract.SettingsBundle{
		OwnerName: v.OwnerName, RootProfile: contract.ProfileName(v.RootProfile),
		RulesMaxChars: v.RulesMaxChars, SubagentMaxDepth: v.SubagentMaxDepth,
		CheckInInterval: contract.DurationMS(v.SessionCheckInInterval.Milliseconds()),
		ShellSilence:    contract.DurationMS((time.Duration(v.ShellSilenceMinutes) * time.Minute).Milliseconds()),
	}, nil
}
