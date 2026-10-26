package subagent

import (
	"context"
	"fmt"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionconv"
)

// sourceConverted marks a transfer that writes the source conversation as a
// session file for another harness and resumes that file.
const sourceConverted = "converted"

// harnessVersion returns the installed version of one harness. The converter
// pins its writers to versions it knows, so a different installed version
// falls back to the written handoff.
func (m *Manager) harnessVersion(harness string) (string, error) {
	if m.InstalledVersion != nil {
		return m.InstalledVersion(harness)
	}
	return harnessruntime.InstalledVersion(harness)
}

// convertPiToClaude writes the source Pi session as a Claude Code session for
// the target profile and returns the new Claude session id. The source file is
// never changed. Every error is a fallback reason: the transfer then builds
// the bounded written handoff instead.
func (m *Manager) convertPiToClaude(ctx context.Context, meta Meta, t TransferState, target profile.Profile) (string, error) {
	sourceVersion := ""
	if meta.RuntimePin != nil {
		sourceVersion = meta.RuntimePin.Bundle.Version
	}
	if sourceVersion == "" {
		version, err := m.harnessVersion(profile.HarnessPi)
		if err != nil {
			return "", err
		}
		sourceVersion = version
	}
	if !sessionconv.Supported(sessionconv.HarnessPi, sourceVersion) {
		return "", fmt.Errorf("Pi %s is not a supported source; this release converts Pi %s", sourceVersion, sessionconv.PiSupportedVersion)
	}
	targetVersion, err := m.harnessVersion(profile.HarnessClaudeCode)
	if err != nil {
		return "", err
	}
	if !sessionconv.Supported(sessionconv.HarnessClaude, targetVersion) {
		return "", fmt.Errorf("Claude Code %s is not a supported target; this release writes Claude Code %s", targetVersion, sessionconv.ClaudeSupportedVersion)
	}
	path, err := harnessruntime.PiSessionPath(meta.HarnessSession)
	if err != nil {
		return "", err
	}
	conversation, err := sessionconv.ReadPi(path)
	if err != nil {
		return "", err
	}
	runDir, _ := m.EffectiveDir(meta)
	target.Env = cloneEnv(target.Env)
	target.Env[mode.EnvRoomID] = meta.RoomID
	historyDir, err := harnessruntime.ClaudeHistoryDir(ctx, target)
	if err != nil {
		return "", err
	}
	id := t.ConvertedSession
	if id == "" {
		id = sessionconv.NewSessionID()
	}
	if _, err := sessionconv.WriteClaude(historyDir, runDir, id, t.Model, conversation); err != nil {
		return "", err
	}
	return id, nil
}
