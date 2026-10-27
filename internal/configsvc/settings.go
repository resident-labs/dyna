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

	"dyna-agent/internal/admission"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/browserconfig"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
)

// Item is one settings key with its effective value and where it came from.
// Kind, Options and Help describe how a client edits it.
type Item struct {
	Key           string `json:"key"`
	Value         any    `json:"value"`
	Source        string `json:"source"`
	LiveVsRestart string `json:"liveVsRestart"`
	// Kind is bool, enum, select, int, bytes or duration.
	Display      string   `json:"display,omitempty"`
	Kind         string   `json:"kind"`
	Options      []string `json:"options,omitempty"`
	OptionLabels []string `json:"optionLabels,omitempty"`
	Help         string   `json:"help"`
}

var help = map[string]string{
	settings.KeyVoiceDucking:           "Lower Voice by 10 dB after sustained speech above the microphone noise floor. Off by default. Applies to the next call; server speech detection still controls interruption.",
	settings.KeyVoiceAgent:             "Let agents use voice: dyna voice say and dyna voice session. On by default. Both spend the voice provider's quota, such as the ChatGPT sign-in's. Turn it off to stop them; owner calls are not affected. Applies to the next say or session.",
	settings.KeyVoiceProvider:          "The realtime voice provider for the next call: openai (the ChatGPT sign-in), speech-to-speech (a self-hosted server), dashscope (Alibaba Cloud, with DASHSCOPE_API_KEY) google-live (Gemini Live, with GOOGLE_API_KEY or GEMINI_API_KEY) doubao-seeduplex (Volcengine, with DOUBAO_API_KEY) stepfun (StepFun, with STEPFUN_API_KEY) or minicpm-o (a self-hosted MiniCPM-o server at voice.url; it cannot use tools, so Voice cannot dispatch work on it).",
	settings.KeyVoiceModel:             "The voice provider's model, such as qwen3.5-omni-plus-realtime for dashscope. Empty uses the provider's default.",
	settings.KeyVoiceURL:               "The voice provider's WebSocket address. Required for speech-to-speech, such as ws://host:8000/v1/realtime; for dashscope it overrides the public endpoint, as a Model Studio workspace model needs.",
	settings.KeyComputersEnabled:       "Give agent sessions a lazy Linux computer when labwc is installed. Applies to the next harness turn.",
	settings.KeyComputersReminder:      "Minutes between reminders to a session whose computer is open: look at the screen with dyna computer shot, and stop the computer when done. A computer with keep on, one with a viewer, and a root computer get none. 0 turns reminders off. Applies to the next reminder.",
	settings.KeySessionCheckInInterval: "Parent check-in interval (default 15m). Zero disables check-ins.",
	settings.KeyAdmissionMode:          "Watch the machine and record when a limit on concurrent agent turns would have held new turns (observe, the default), or turn the watcher off. Nothing is ever held. While memory is short, the watcher also stops up to 3 agent processes per sample that have sat idle for 5 minutes or more, longest first, and never one with a turn running, queued or waiting for input; the session resumes in a new process on its next turn. The engine samples about every 45 seconds. Applies to the next sample.",
	settings.KeyDoctorCondition:        "When the doctor hook examines a machine, such as \"load>2/core for 5m or memory>85%\". Empty uses the default. Applies to the next doctor run.",
	settings.KeyLibraryEnabled:         "JSON list of main checkout paths whose librarian runs. Use dyna library enable and disable, which also add and remove the librarian hooks.",
	settings.KeyLibraryProfile:         "One profile for all librarian work. Auto ranks enabled service profiles.",
	settings.KeyLibrarySmellsPerDay:    "Most smells the librarian files for one repository in a UTC day (default 10). Zero means no cap. A pass started with dyna library wake --smells --now has no cap. Applies to the next smell.",
	settings.KeyLibraryReadmeMaxLines:  "Longest README.md the librarian may leave (default 300 lines). Its README change fails dyna library check-readme when it is longer.",
	settings.KeyMaxCardBytes:           "Maximum HTML/JavaScript card size (default 25 MiB). Accepts 25 MiB, 25MB, 25m or a byte count; MB means MiB. Applies to the next operation, without restart.",
	settings.KeyMaxFileBytes:           "Maximum file attachment size (default 25 MiB). Accepts 25 MiB, 25MB, 25m or a byte count; MB means MiB. Post total is derived for eight files or one card and seven files. Applies to the next operation.",
	settings.KeyAutoCheck:              "Check for a new dyna release at most once a day and tell you about it.",
	settings.KeyRunsRetention:          "Delete archived workflow runs after this time without changes. Use 0 or off to disable deletion.",
	settings.KeyWorktreesRetention:     "Which isolated worktrees a workflow keeps after an agent ends: changed ones, all, or none.",
	settings.KeyLocalCIRemote:          "SSH destination and absolute cache directory for local CI. Applies to the next CI run.",
	settings.KeyWorktreesRoot:          "Directory for new managed worktrees. Defaults to ~/.dyna-worktrees. Applies to the next worktree creation.",
	settings.KeyWorktreesMinFreeBytes:  "Minimum free disk space before worktree maintenance runs garbage collection. Defaults to 10 GiB. Accepts byte counts and sizes such as 10 GiB; zero disables the free-space trigger. A RAM-backed file system such as /tmp is judged by its share of free space instead.",
	settings.KeyRootProfile:            "Profile for new internal root sessions (default opus). Existing roots keep their profile. Missing or disabled profiles use the default profile.",
	settings.KeySubagentMaxDepth:       "How deep sessions may nest. Your own sessions are depth 1.",
	settings.KeyCIDiskFloorGB:          "Free space a CI machine must keep on the file system that holds its /var/tmp, where Dyna keeps CI work, in GiB (1 GiB = 1,073,741,824 bytes; the key keeps the shorter GB name). A machine below the floor starts no new CI work until space is freed, and a run below it removes Dyna's own caches first. Defaults to 20 GiB; zero turns the floor off.",
	settings.KeyIdleUnloadMinutes:      "Minutes an idle session's Pi or Claude process stays loaded before Dyna stops it to free memory. The next turn resumes the same session in a new process, which takes a few seconds longer to start. 0 keeps processes loaded. Applies to the next process Dyna starts.",
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
		{Key: settings.KeyPRTrustedBots, Value: v.PRTrustedBots, Source: v.Source(settings.KeyPRTrustedBots), LiveVsRestart: settings.LiveNextOperation, Kind: "string", Help: "JSON list of trusted GitHub bot logins. Other outside comments are held for the owner."},
		{Key: settings.KeyVoiceDucking, Value: v.VoiceDucking, Source: v.Source(settings.KeyVoiceDucking), LiveVsRestart: settings.LiveNextOperation, Kind: "bool"},
		{Key: settings.KeyVoiceAgent, Value: v.VoiceAgent, Source: v.Source(settings.KeyVoiceAgent), LiveVsRestart: settings.LiveNextOperation, Kind: "bool"},
		{Key: settings.KeyLocalLaneRepos, Value: v.LocalLaneRepos, Source: v.Source(settings.KeyLocalLaneRepos), LiveVsRestart: settings.LiveNextOperation, Kind: "string", Help: "JSON list of absolute repository paths using the local lane. Fetches every five minutes."},
		{Key: settings.KeyComputersEnabled, Value: v.ComputersEnabled, Source: v.Source(settings.KeyComputersEnabled), LiveVsRestart: settings.LiveNextOperation, Kind: "bool"},
		{Key: settings.KeyComputersReminder, Value: v.ComputersReminderMinutes, Source: v.Source(settings.KeyComputersReminder), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeySessionCheckInInterval, Value: v.SessionCheckInInterval.String(), Source: v.Source(settings.KeySessionCheckInInterval), LiveVsRestart: settings.LiveNextOperation, Kind: "duration"},
		{Key: settings.KeyAdmissionMode, Value: v.AdmissionMode, Source: v.Source(settings.KeyAdmissionMode), LiveVsRestart: settings.LiveNextOperation, Kind: "enum", Options: []string{string(settings.AdmissionOff), string(settings.AdmissionObserve)}, Display: admissionSummary(v)},
		{Key: settings.KeyDoctorCondition, Value: v.DoctorCondition, Source: v.Source(settings.KeyDoctorCondition), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyLibraryEnabled, Value: v.LibraryEnabled, Source: v.Source(settings.KeyLibraryEnabled), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyLibraryProfile, Value: v.LibraryProfile, Source: v.Source(settings.KeyLibraryProfile), LiveVsRestart: settings.LiveNextOperation, Kind: "select", Options: options, OptionLabels: labels},
		{Key: settings.KeyLibrarySmellsPerDay, Value: v.LibrarySmellsPerDay, Source: v.Source(settings.KeyLibrarySmellsPerDay), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyLibraryReadmeMaxLines, Value: v.LibraryReadmeMaxLines, Source: v.Source(settings.KeyLibraryReadmeMaxLines), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyMaxCardBytes, Value: v.MaxCardBytes, Source: v.Source(settings.KeyMaxCardBytes), LiveVsRestart: settings.LiveNextOperation, Kind: "bytes"},
		{Key: settings.KeyMaxFileBytes, Value: v.MaxFileBytes, Source: v.Source(settings.KeyMaxFileBytes), LiveVsRestart: settings.LiveNextOperation, Kind: "bytes"},
		{Key: settings.KeyAutoCheck, Value: v.EffectiveAutoCheck(), Source: v.Source(settings.KeyAutoCheck), LiveVsRestart: settings.LiveRestart, Kind: "bool"},
		{Key: settings.KeyRunsRetention, Value: v.RunsRetention.String(), Source: v.Source(settings.KeyRunsRetention), LiveVsRestart: settings.LiveNextOperation, Kind: "duration"},
		{Key: settings.KeyWorktreesRetention, Value: v.WorktreesRetention, Source: v.Source(settings.KeyWorktreesRetention), LiveVsRestart: settings.LiveNextOpen, Kind: "enum", Options: []string{settings.WorktreesRetentionChanged, settings.WorktreesRetentionAll, settings.WorktreesRetentionNone}},
		{Key: settings.KeyLocalCIRemote, Value: v.LocalCIRemote, Source: v.Source(settings.KeyLocalCIRemote), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyCIDiskFloorGB, Value: v.CIDiskFloorGB, Source: v.Source(settings.KeyCIDiskFloorGB), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeyWorktreesRoot, Value: v.WorktreesRoot, Source: v.Source(settings.KeyWorktreesRoot), LiveVsRestart: settings.LiveNextOperation, Kind: "string"},
		{Key: settings.KeyWorktreesMinFreeBytes, Value: v.WorktreesMinFreeBytes, Source: v.Source(settings.KeyWorktreesMinFreeBytes), LiveVsRestart: settings.LiveNextOperation, Kind: "bytes"},
		{Key: settings.KeyRootProfile, Value: v.RootProfile, Source: v.Source(settings.KeyRootProfile), LiveVsRestart: settings.LiveNextOpen, Kind: "select", Options: rootOptions, OptionLabels: rootLabels},
		{Key: settings.KeySubagentMaxDepth, Value: v.SubagentMaxDepth, Source: v.Source(settings.KeySubagentMaxDepth), LiveVsRestart: settings.LiveNextOpen, Kind: "int"},
		{Key: settings.KeyIdleUnloadMinutes, Value: v.HarnessIdleUnloadMinutes, Source: v.Source(settings.KeyIdleUnloadMinutes), LiveVsRestart: settings.LiveNextOperation, Kind: "int"},
		{Key: settings.KeySteeringToolWait, Value: v.SteeringMaxToolWait.String(), Source: v.Source(settings.KeySteeringToolWait), LiveVsRestart: settings.LiveNextOpen, Kind: "duration"},
		{Key: settings.KeyVoiceProvider, Value: v.VoiceProvider, Source: v.Source(settings.KeyVoiceProvider), LiveVsRestart: settings.LiveNextOperation, Kind: "enum", Options: settings.VoiceProviders},
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
		modeHelp += " Copy my profile is unavailable in WSL and systemd-nspawn because the owner keyring is unavailable."
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

// admissionSummary tells the owner what the turn limit did in the last day, so
// the choice to enforce rests on a count and not a guess.
func admissionSummary(v settings.Values) string {
	mode := string(v.AdmissionMode)
	if v.AdmissionMode == settings.AdmissionOff {
		return mode
	}
	sum, err := admission.Observed{DataDir: store.DataDir()}.Summarize(time.Now().Add(-24 * time.Hour))
	if err != nil {
		return mode
	}
	text := fmt.Sprintf("%s · a turn limit would have held new turns for about %d min in the last 24 h", mode, int(sum.Held().Minutes()))
	if sum.MostOver > 0 {
		text += fmt.Sprintf(", up to %d over the limit", sum.MostOver)
	}
	return text
}
