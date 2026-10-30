package runtime

import (
	"dyna-agent/internal/account/store"
	"time"
)

type PrivateRuntimeSnapshot struct {
	Bundles             []Bundle             `json:"bundles" tstype:"Bundle[] | null"`
	Accounts            []NativeAccount      `json:"accounts" tstype:"NativeAccount[] | null"`
	Profiles            []Doctor             `json:"profiles" tstype:"Doctor[] | null"`
	Capabilities        []Capability         `json:"capabilities" tstype:"Capability[] | null"`
	SelectedVersions    map[string]string    `json:"selectedVersions" tstype:"HarnessVersions"`
	RecommendedVersions []RecommendedVersion `json:"recommendedVersions" tstype:"RecommendedVersion[] | null"`
	// Installing lists harnesses whose CLI is being installed on first use.
	Installing []string `json:"installing" tstype:"string[] | null"`
}

type PrivateLogin struct {
	AccountID string `json:"accountId"`
	// Harness names the harness of a sign-in that has no account of its own.
	Harness string `json:"harness,omitempty"`
	// Target names the saved account a sign-in again writes, for a flow that has
	// no account of its own, such as Pi's provider sign-in.
	Target  string       `json:"target,omitempty"`
	Warning string       `json:"warning,omitempty"`
	SignIn  *SignInState `json:"signIn,omitempty"`
}

type Doctor struct {
	Profile       string   `json:"profile"`
	Harness       string   `json:"harness"`
	Binary        string   `json:"binary"`
	Version       string   `json:"version"`
	Home          string   `json:"home"`
	AccountID     string   `json:"accountId"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	SwitchPending bool     `json:"switchPending"`
	RunningTurns  int      `json:"runningTurns"`
	Warnings      []string `json:"warnings,omitempty"`
}

type Capability struct {
	Harness             string `json:"harness"`
	Status              string `json:"status"`
	Reason              string `json:"reason"`
	InstallSupported    bool   `json:"installSupported"`
	AccountBlocked      string `json:"accountBlocked"`
	MCP                 bool   `json:"mcp"`
	Concurrent          bool   `json:"concurrent"`
	SharedPersonalLogin bool   `json:"sharedPersonalLogin"`
}

type NativeAccount struct {
	prepared  *preparedAccount
	ID        string         `json:"id"`
	Harness   string         `json:"harness"`
	Provider  store.Provider `json:"provider,omitempty"`
	Kind      store.Kind     `json:"kind,omitempty"`
	Label     string         `json:"label"`
	Home      string         `json:"home"`
	Identity  string         `json:"identity"`
	Status    string         `json:"status"`
	Active    bool           `json:"active"`
	ExpiresAt *time.Time     `json:"expiresAt,omitempty"`
	// Repos lists the repositories this account is limited to; empty means any.
	Repos []string `json:"repos" tstype:"string[] | null"`
}

// RecommendedVersion describes an install candidate, not an installed bundle.
// Only pinned and resolved results have a Version. A resolved npm version has
// verified package metadata, but its artifact is still checked during install.
type RecommendedVersion struct {
	Harness string `json:"harness"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source"`
	Status  string `json:"status"` // pinned, resolved, unavailable, or unsupported
	Reason  string `json:"reason,omitempty"`
}

// SignInChoice is one native OAuth provider or login-method choice.
type SignInChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SignInState is the in-app view of one native sign-in. It holds the link and
// the one-time user code, which the owner needs, and never token contents.
type SignInState struct {
	Choices      []SignInChoice `json:"choices,omitempty"`
	Phase        string         `json:"phase"`
	URL          string         `json:"url,omitempty"`
	Code         string         `json:"code,omitempty"`
	Message      string         `json:"message,omitempty"`
	AcceptsPaste bool           `json:"acceptsPaste,omitempty"`
	SecretInput  bool           `json:"secretInput,omitempty"`
}

// PersonalLogin is the owner's own native login for one harness. Saved means
// that a Dyna account already holds the same identity.
type PersonalLogin struct {
	Harness  string `json:"harness"`
	Label    string `json:"label"`
	Saved    bool   `json:"saved"`
	identity string
}

type PreferenceKey struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

type PreferencePreview struct {
	AccountID string          `json:"accountId"`
	Harness   string          `json:"harness"`
	Hash      string          `json:"hash"`
	Keys      []PreferenceKey `json:"keys" tstype:"PreferenceKey[] | null"`
}
