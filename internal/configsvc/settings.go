// Package configsvc reads and edits the owner's Dyna configuration: the
// settings keys and the worker profiles. The CLI (`dyna settings`) and the
// desktop Settings view call it, so both always show and write the same
// thing. It returns plain JSON data and never returns a stored secret.
package configsvc

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/browserconfig"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/settings"
)

var help = map[string]string{
	settings.KeyVoiceDucking:           "Lower Voice by 10 dB while you speak. Off by default. Applies to the next call.",
	settings.KeyVoiceAgent:             "Let agents use voice through dyna voice say and dyna voice session. On by default. Their calls spend the voice provider's quota.",
	settings.KeyVoiceProvider:          "The realtime voice provider for the next call.",
	settings.KeyVoiceModel:             "The voice provider's model, such as qwen3.5-omni-plus-realtime for dashscope. Empty uses the provider's default.",
	settings.KeyVoiceURL:               "The provider's WebSocket address, such as ws://host:8000/v1/realtime. Required for speech-to-speech; for dashscope it overrides the public endpoint.",
	settings.KeyComputersEnabled:       "Give agent sessions a lazy Linux computer when labwc is installed. Applies to the next harness turn.",
	settings.KeyShellSilenceMinutes:    "Minutes without local shell output before a notice. 0 turns notices off. Turn and workflow step output is not watched yet.",
	settings.KeyComputersReminder:      "Minutes between reminders to check and stop an open computer. Computers kept on, with a viewer, or owned by root get none. 0 turns reminders off.",
	settings.KeyComputersRenderNode:    "GPU for Dyna computers: auto, or a /dev/dri/renderD* path. auto avoids the GPU that drives the display when another exists. Applies to the next computer start.",
	settings.KeySessionCheckInInterval: "Parent check-in interval (default 15m). Zero disables check-ins.",
	settings.KeyDoctorCondition:        "When the doctor hook examines a machine, such as \"load>2/core for 5m or memory>85%\". Empty uses the default. Applies to the next doctor run.",
	settings.KeyLibraryEnabled:         "JSON list of main checkout paths whose librarian runs. Use dyna library enable and disable, which also add and remove the librarian hooks.",
	settings.KeyLibraryProfile:         "Default librarian profile when a room has no pin. Auto ranks enabled service profiles. A room pin is dyna services profile library.",
	settings.KeyLibrarySmellsPerDay:    "Most smells filed for one repository each UTC day (default 10). Zero means no cap, and so does dyna library wake --smells --now.",
	settings.KeyLibraryReadmeMaxLines:  "Longest README.md the librarian may leave (default 300 lines). Its README change fails dyna library check-readme when it is longer.",
	settings.KeyRulesMaxChars:          "Most characters the owner allows in room rules, and in global rules, each (default 3000, at least 500). Applies to the next rule change or rules list.",
	settings.KeyMaxCardBytes:           "Largest HTML/JavaScript card (default 25 MiB). Accepts 25 MiB, 25MB, 25m or a byte count; MB means MiB. Applies to the next operation.",
	settings.KeyMaxFileBytes:           "Largest file attachment (default 25 MiB). Accepts 25 MiB, 25MB, 25m or a byte count; MB means MiB. Applies to the next operation.",
	settings.KeyAutoCheck:              "Check for a new dyna release at most once a day and tell you about it.",
	settings.KeyDatabaseCallerDetail:   "Count database writes by caller for the engine status. Adds a stack walk to each write; off by default.",
	settings.KeyRunsRetention:          "Delete archived workflow runs after this time without changes. Use 0 or off to disable deletion.",
	settings.KeyWorktreesRetention:     "Which isolated worktrees a workflow keeps after an agent ends: changed ones, all, or none.",
	settings.KeyLocalCIRemote:          "SSH destination and absolute cache directory for local CI. Applies to the next CI run.",
	settings.KeyWorktreesRoot:          "Directory for new managed worktrees. Defaults to ~/.dyna-worktrees. Applies to the next worktree creation.",
	settings.KeyWorktreesMinFreeBytes:  "Free disk space below which worktree cleanup runs (default 10 GiB). Zero turns this check off. A RAM-backed file system such as /tmp uses its share of free space.",
	settings.KeyRootProfile:            "Profile for new internal root sessions (default opus). Existing roots keep theirs. A missing or disabled profile uses the default.",
	settings.KeySubagentMaxDepth:       "How deep sessions may nest. Your own sessions are depth 1.",
	settings.KeyPiCodeMode:             "Pi code mode: only routes tools through codemode; off restores Pi's previous behavior. Applies to new processes.",
	settings.KeyIdleUnloadMinutes:      "Minutes before an idle Pi or Claude process unloads to free memory. Its next turn starts a new process. 0 keeps processes loaded.",
	settings.KeySteeringToolWait:       "How long a message to a busy session waits for a running tool before it interrupts the tool.",
}

// Items lists every settings key, in the order `dyna settings list` prints.
func Items() ([]Item, error) {
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return nil, err
	}
	profiles, _ := profile.Load(profile.DefaultPath())
	options, labels := []string{""}, []string{"Auto · best taste + cost (none)"}
	if profiles != nil {
		enabled := rolesession.RankProfiles(slices.DeleteFunc(slices.Clone(profiles.Profiles), func(p profile.Profile) bool { return !p.ForServices() }), nil, time.Now(), "")
		if len(enabled) > 0 {
			labels[0] = "Auto · best taste + cost (" + enabled[0].Name + ")"
		}
		for _, p := range enabled {
			options = append(options, p.Name)
			labels = append(labels, fmt.Sprintf("%s · thrift %d", p.Name, p.Cost))
		}
	}
	voiceNeeds := make([]string, 0, len(settings.VoiceProviders))
	for _, name := range settings.VoiceProviders {
		voiceNeeds = append(voiceNeeds, settings.VoiceProviderNeeds[name])
	}
	rootOptions, rootLabels := []string{""}, []string{"Default profile"}
	if profiles != nil {
		for _, p := range profiles.Profiles {
			if p.ForSubagents() {
				rootOptions = append(rootOptions, p.Name)
				rootLabels = append(rootLabels, p.Name)
			}
		}
	}
	items := []Item{
		{Key: settings.KeyPRTrustedBots, Value: v.PRTrustedBots, Source: v.Source(settings.KeyPRTrustedBots), LiveVsRestart: settings.LiveNextOperation, Kind: "string", Help: "JSON list of trusted GitHub bot logins. Other outside comments follow pr.outsideComments."},
		{Key: settings.KeyPROutsideComments, Value: string(v.PROutsideComments), Source: v.Source(settings.KeyPROutsideComments), LiveVsRestart: settings.LiveNextOperation, Kind: "enum", Options: []string{string(settings.OutsideCommentsDeliver), string(settings.OutsideCommentsHold)}, Help: "deliver sends outside comments to the author as untrusted data. hold keeps them until the owner admits them."},
		{Key: settings.KeyVoiceDucking, Value: v.VoiceDucking, Source: v.Source(settings.KeyVoiceDucking), LiveVsRestart: settings.LiveNextOperation, Kind: "bool"},
		{Key: settings.KeyVoiceAgent, Value: v.VoiceAgent, Source: v.Source(settings.KeyVoiceAgent), LiveVsRestart: settings.LiveNextOperation, Kind: "bool"},
		{Key: settings.KeyLocalLaneRepos, Value: v.LocalLaneRepos, Source: v.Source(settings.KeyLocalLaneRepos), LiveVsRestart: settings.LiveNextOperation, Kind: "string", Help: "JSON list of absolute repository paths using the local lane. Fetches every five minutes."},
		{Key: settings.KeyComputersEnabled, Value: v.ComputersEnabled, Source: v.Source(settings.KeyComputersEnabled), LiveVsRestart: settings.LiveNextOperation, Kind: "bool"},
		{Key: settings.KeyShellSilenceMinutes, Value: v.ShellSilenceMinutes, Source: v.Source(settings.KeyShellSilenceMinutes), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyComputersReminder, Value: v.ComputersReminderMinutes, Source: v.Source(settings.KeyComputersReminder), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyComputersRenderNode, Value: v.ComputersRenderNode, Source: v.Source(settings.KeyComputersRenderNode), LiveVsRestart: settings.LiveNextOpen, Kind: "string"},
		{Key: settings.KeySessionCheckInInterval, Value: v.SessionCheckInInterval.String(), Source: v.Source(settings.KeySessionCheckInInterval), LiveVsRestart: settings.LiveNextOperation, Kind: "duration"},
		{Key: settings.KeyDoctorCondition, Value: v.DoctorCondition, Source: v.Source(settings.KeyDoctorCondition), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyLibraryEnabled, Value: v.LibraryEnabled, Source: v.Source(settings.KeyLibraryEnabled), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyLibraryProfile, Value: v.LibraryProfile, Source: v.Source(settings.KeyLibraryProfile), LiveVsRestart: settings.LiveNextOperation, Kind: "select", Options: options, OptionLabels: labels},
		{Key: settings.KeyLibrarySmellsPerDay, Value: v.LibrarySmellsPerDay, Source: v.Source(settings.KeyLibrarySmellsPerDay), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyLibraryReadmeMaxLines, Value: v.LibraryReadmeMaxLines, Source: v.Source(settings.KeyLibraryReadmeMaxLines), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyRulesMaxChars, Value: v.RulesMaxChars, Source: v.Source(settings.KeyRulesMaxChars), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyMaxCardBytes, Value: v.MaxCardBytes, Source: v.Source(settings.KeyMaxCardBytes), LiveVsRestart: settings.LiveNextOperation, Kind: "bytes"},
		{Key: settings.KeyMaxFileBytes, Value: v.MaxFileBytes, Source: v.Source(settings.KeyMaxFileBytes), LiveVsRestart: settings.LiveNextOperation, Kind: "bytes"},
		{Key: settings.KeyAutoCheck, Value: v.EffectiveAutoCheck(), Source: v.Source(settings.KeyAutoCheck), LiveVsRestart: settings.LiveRestart, Kind: "bool"},
		{Key: settings.KeyDatabaseCallerDetail, Value: v.DatabaseCallerDetail, Source: v.Source(settings.KeyDatabaseCallerDetail), LiveVsRestart: settings.LiveRestart, Kind: "bool"},
		{Key: settings.KeyRunsRetention, Value: v.RunsRetention.String(), Source: v.Source(settings.KeyRunsRetention), LiveVsRestart: settings.LiveNextOperation, Kind: "duration"},
		{Key: settings.KeyWorktreesRetention, Value: v.WorktreesRetention, Source: v.Source(settings.KeyWorktreesRetention), LiveVsRestart: settings.LiveNextOpen, Kind: "enum", Options: []string{settings.WorktreesRetentionChanged, settings.WorktreesRetentionAll, settings.WorktreesRetentionNone}},
		{Key: settings.KeyLocalCIRemote, Value: v.LocalCIRemote, Source: v.Source(settings.KeyLocalCIRemote), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyWorktreesRoot, Value: v.WorktreesRoot, Source: v.Source(settings.KeyWorktreesRoot), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyWorktreesMinFreeBytes, Value: v.WorktreesMinFreeBytes, Source: v.Source(settings.KeyWorktreesMinFreeBytes), LiveVsRestart: settings.LiveNextOperation, Kind: "bytes"},
		{Key: settings.KeyRootProfile, Value: v.RootProfile, Source: v.Source(settings.KeyRootProfile), LiveVsRestart: settings.LiveNextOpen, Kind: "select", Options: rootOptions, OptionLabels: rootLabels},
		{Key: settings.KeySubagentMaxDepth, Value: v.SubagentMaxDepth, Source: v.Source(settings.KeySubagentMaxDepth), LiveVsRestart: settings.LiveNextOpen, Kind: "int"},
		{Key: settings.KeyPiCodeMode, Value: v.PiCodeMode, Source: v.Source(settings.KeyPiCodeMode), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyIdleUnloadMinutes, Value: v.HarnessIdleUnloadMinutes, Source: v.Source(settings.KeyIdleUnloadMinutes), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeySteeringToolWait, Value: v.SteeringMaxToolWait.String(), Source: v.Source(settings.KeySteeringToolWait), LiveVsRestart: settings.LiveNextOpen, Kind: "duration"},
		{Key: settings.KeyVoiceProvider, Value: v.VoiceProvider, Source: v.Source(settings.KeyVoiceProvider), LiveVsRestart: settings.LiveNextOperation, Kind: "enum", Options: settings.VoiceProviders, OptionNotes: voiceNeeds},
		{Key: settings.KeyVoiceModel, Value: v.VoiceModel, Source: v.Source(settings.KeyVoiceModel), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyVoiceURL, Value: v.VoiceURL, Source: v.Source(settings.KeyVoiceURL), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
	}
	items = append(items, browserItems(v, browserconfig.CopyMineAvailable(), browserconfig.Profiles())...)
	for i := range items {
		if items[i].Help == "" {
			items[i].Help = help[items[i].Key]
		}
		switch items[i].Kind {
		case "bytes":
			items[i].Display = settings.FormatBytes(items[i].Value.(int64))
		case "duration":
			duration, err := time.ParseDuration(fmt.Sprint(items[i].Value))
			if err != nil {
				return nil, err
			}
			items[i].Display = settings.FormatDuration(duration)
			if items[i].Key == settings.KeyRunsRetention && duration > 0 && duration%(24*time.Hour) == 0 {
				items[i].Display = fmt.Sprintf("%dd", duration/(24*time.Hour))
			}
		}
		if items[i].Key == settings.KeyMaxCardBytes || items[i].Key == settings.KeyMaxFileBytes {
			limits := settings.ContentLimits{CardBytes: v.MaxCardBytes, FileBytes: v.MaxFileBytes}
			items[i].Help += fmt.Sprintf(" Current post total: %s = 7 × file limit + max(file limit, card limit).", settings.FormatBytes(limits.PostBytes()))
		}
	}
	return items, nil
}

// Set validates and writes one settings key, then returns its new item.
func Set(ctx context.Context, key, value string) (Item, error) {
	if err := authority.Check("settings set", authority.Of(ctx), nil); err != nil {
		return Item{}, err
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	// The global librarian default affects every room that has no pin, so only
	// the owner may change it. A root sets its own room with dyna services profile.
	if key == settings.KeyLibraryProfile && authority.Of(ctx).Role != authority.Owner {
		return Item{}, fmt.Errorf("library.profile is the default for rooms with no pin; set your room with dyna services profile library, or ask the owner for every room")
	}
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return Item{}, err
	}
	if key == settings.KeyDoctorCondition && value != "" {
		if _, err := doctor.ParseCondition(value); err != nil {
			return Item{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	if err := v.Set(key, value); err != nil {
		return Item{}, err
	}
	if err := v.Save(); err != nil {
		return Item{}, err
	}
	items, err := Items()
	if err != nil {
		return Item{}, err
	}
	for _, it := range items {
		if it.Key == key {
			return it, nil
		}
	}
	return Item{Key: key}, nil
}

// browserItems keeps unavailable stored values visible without offering them as choices.
func browserItems(v settings.Values, copyMine bool, profiles []browserconfig.Profile) []Item {
	modes := []string{"shared", "separate"}
	labels := []string{"Shared Dyna profile", "Separate profile for each computer"}
	modeHelp := "Each computer uses its own working copy of the selected profile."
	if copyMine {
		modes = append(modes, "copy-mine")
		labels = append(labels, "Copy my Chromium profile")
	} else {
		modeHelp += " Copy my profile is unavailable here: WSL and systemd-nspawn have no owner keyring."
	}
	options, names := []string{}, []string{}
	for _, p := range profiles {
		options = append(options, p.Directory)
		names = append(names, p.Name+" · "+p.Directory)
	}
	item := func(key string, value any, kind, help string) Item {
		return Item{Key: key, Value: value, Kind: kind, Source: v.Source(key), LiveVsRestart: settings.LiveNextOperation, Help: help}
	}
	mode := item(settings.KeyBrowserProfileMode, v.BrowserProfileMode, "select", modeHelp)
	mode.Options, mode.OptionLabels = modes, labels
	owner := item(settings.KeyBrowserOwnerProfile, v.BrowserOwnerProfile, "select", "Start from a copy of this Chromium profile. Dyna never writes back to your own profile.")
	owner.Options, owner.OptionLabels = options, names
	return []Item{
		item(settings.KeyBrowserPath, v.BrowserPath, "string", "Leave empty to find Chromium automatically."),
		item(settings.KeyBrowserExtraFlags, v.BrowserExtraFlags, "string", "Extra Chromium arguments. Use quotes around values that contain spaces."),
		item(settings.KeyBrowserUseFlagsFile, v.BrowserUseFlagsFile, "bool", "Read your chromium-flags.conf when Chromium starts."),
		mode, owner,
		item(settings.KeyBrowserSaveBack, v.BrowserSaveBack, "bool", "Save login state to the shared Dyna profile when a computer stops. The last computer to save wins."),
	}
}
