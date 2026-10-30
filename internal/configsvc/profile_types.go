package configsvc

import "dyna-agent/internal/profile"

// ProfileView is a profile as an editor shows it. Env values never leave
// Go: they often hold API keys. The editor sees only which keys are set.
//
// ExtraArgs and Command do reach the editor, and they can carry a token
// (`--api-key ...`). So the whole configsvc API is for the owner only: a
// web server must put every config call behind its token, with no
// exceptions. roomsvc.ProfileChoice is the only profile shape that is safe
// to serve without one.
type ProfileView struct {
	RuntimeVersion string   `json:"runtimeVersion"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Harness        string   `json:"harness"`
	Model          string   `json:"model"`
	Models         []string `json:"models,omitempty"`
	// Fallback names the profile that continues a session when this profile's
	// harness hits a usage limit with no account left. The next profile's own
	// fallback continues the chain.
	Fallback       string   `json:"fallback"`
	Taste          int      `json:"taste"`
	Intelligence   int      `json:"intelligence"`
	Cost           int      `json:"cost"`
	Diagnostics    int      `json:"diagnostics"`
	TimeoutSec     int      `json:"timeoutSec"`
	MaxConcurrent  int      `json:"maxConcurrent"`
	MaxCallsPerRun int      `json:"maxCallsPerRun"`
	Default        bool     `json:"default"`
	Disabled       bool     `json:"disabled"`
	Uses           []string `json:"uses" tstype:"string[] | null"`
	SafeMode       bool     `json:"safeMode"`
	ExtraArgs      []string `json:"extraArgs" tstype:"string[] | null"`
	// Compact is the auto-compact threshold; null keeps the harness default.
	Compact *profile.Compact `json:"compact" tstype:"Compact | null,required"`
	EnvKeys []string         `json:"envKeys" tstype:"string[] | null"`
	// Support is how much of the harness's output Dyna reads. It is derived
	// from the profile's output dialect and ignored on save.
	Support HarnessSupport `json:"support"`
	// Problem is the typed reason this profile cannot run, so the editor can
	// offer repair instead of refusing the whole list. Empty means launchable.
	Problem profile.Status `json:"problem,omitempty"`
}

// ProfileList is every profile plus the harnesses a profile may use.
type ProfileList struct {
	Profiles  []ProfileView `json:"profiles" tstype:"ProfileView[] | null"`
	Harnesses []string      `json:"harnesses" tstype:"string[] | null"`
	// Unready maps each harness a new profile cannot use yet to the reason.
	Unready map[string]string `json:"unready" tstype:"StringMapOrNull"`
	// Default is the profile a session starts with when none is named.
	Default string `json:"default"`
}

// ProfileEdit saves one profile. Original names the profile being edited;
// empty creates one. A different Name renames it. Env changes are explicit:
// EnvSet adds or replaces values, EnvRemove drops keys, and every other
// stored key keeps its value unseen.
type ProfileEdit struct {
	Original  string            `json:"original"`
	Profile   ProfileView       `json:"profile"`
	EnvSet    map[string]string `json:"envSet" tstype:"StringMapOrNull"`
	EnvRemove []string          `json:"envRemove" tstype:"string[] | null"`
}

// ProfileCheck returns fresh support and health after an optional live probe.
// ProbeError describes an unsuccessful probe without losing the observed evidence.
type ProfileCheck struct {
	Name       string         `json:"name"`
	Support    HarnessSupport `json:"support"`
	ProbeError string         `json:"probeError,omitempty"`
}
