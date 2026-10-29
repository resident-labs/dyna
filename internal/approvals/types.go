package approvals

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Kind identifies the outside service that a held write uses.
type Kind string

const (
	KindGH Kind = "gh"
)

// State is the lifecycle state of an approval request.
type State string

const (
	StatePending   State = "pending"
	StateApproved  State = "approved"
	StateRunning   State = "running"
	StateDenied    State = "denied"
	StateExpired   State = "expired"
	StateCancelled State = "cancelled"
	StateDone      State = "done"
	StateFailed    State = "failed"
)

// Action is one recorded decision.
type Action string

const (
	ActionApprove Action = "approve"
	ActionDeny    Action = "deny"
	ActionPass    Action = "pass"
	ActionAuto    Action = "auto"
)

const (
	DefaultWait   = 60 * time.Second
	DefaultExpiry = 24 * time.Hour
	Owner         = "owner"
)

var (
	ErrNotFound   = errors.New("approval request not found")
	ErrNotPending = errors.New("approval request is no longer pending")
	ErrForbidden  = errors.New("this caller may not decide the approval request")
)

// FileSnapshot binds a file's contents at request time to its original path.
type FileSnapshot struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

// RequestInput is the ApprovalRequest RPC payload for a GitHub write.
type RequestInput struct {
	Argv  []string       `json:"argv"`
	Cwd   string         `json:"cwd"`
	Repo  string         `json:"repo"`
	Files []FileSnapshot `json:"files,omitempty"`
}

// GHPayload is the exact stored payload of a gh request.
type GHPayload struct {
	Argv  []string       `json:"argv"`
	Cwd   string         `json:"cwd"`
	Repo  string         `json:"repo"`
	Files []FileSnapshot `json:"files,omitempty"`
}

// DisplayBody returns the exact inline or file-backed text shown for approval.
func (p GHPayload) DisplayBody() string {
	if body := argument(p.Argv, "--body"); body != "" {
		return body
	}
	snapshot := func(path string) (string, bool) {
		for _, file := range p.Files {
			if file.Path == path {
				return string(file.Data), true
			}
		}
		return "", false
	}
	path := argument(p.Argv, "--body-file")
	if path == "" {
		path = argument(p.Argv, "-F")
	}
	if body, ok := snapshot(path); ok {
		return body
	}
	var parts []string
	if path := argument(p.Argv, "--input"); path != "" {
		if body, ok := snapshot(path); ok {
			parts = append(parts, body)
		}
	}
	for i := 0; i < len(p.Argv); i++ {
		arg := p.Argv[i]
		value := ""
		switch {
		case arg == "-F" || arg == "--field":
			if i+1 < len(p.Argv) {
				i++
				value = p.Argv[i]
			}
		case strings.HasPrefix(arg, "--field="):
			value = strings.TrimPrefix(arg, "--field=")
		case strings.HasPrefix(arg, "-F"):
			value = strings.TrimPrefix(arg[len("-F"):], "=")
		}
		key, body, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(body, "@") {
			body, _ = snapshot(strings.TrimPrefix(body, "@"))
		}
		parts = append(parts, key+":\n"+body)
	}
	return strings.Join(parts, "\n\n")
}

// CreateInput is the typed input to the kind-independent core.
type CreateInput struct {
	Room       string
	Kind       Kind
	Class      Class
	Payload    json.RawMessage
	Repository string
	Server     string
	Command    string
	Tool       string
	Title      string
	Body       string
}

// Result is the outcome that the requester receives.
type Result struct {
	Actor    string   `json:"actor,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	ExitCode *int     `json:"exitCode,omitempty"`
	URL      string   `json:"url,omitempty"`
	Output   []string `json:"output,omitempty"`
}

// Request is one durable approval request. At is a display name in RPC results.
type Request struct {
	ID          string          `json:"id"`
	Room        string          `json:"room"`
	Requester   string          `json:"requester"`
	Kind        Kind            `json:"kind"`
	Class       Class           `json:"class"`
	Payload     json.RawMessage `json:"payload"`
	PayloadHash string          `json:"payloadHash"`
	State       State           `json:"state"`
	At          string          `json:"at"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	Result      *Result         `json:"result,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	atID        string
}

// Final reports whether no execution or decision remains.
func (r Request) Final() bool {
	switch r.State {
	case StateDenied, StateExpired, StateCancelled, StateDone, StateFailed:
		return true
	}
	return false
}

// DecisionInput is the ApprovalDecide RPC payload.
type DecisionInput struct {
	Room      string `json:"room"`
	RequestID string `json:"requestId"`
	Action    Action `json:"action"`
	Reason    string `json:"reason"`
}

// Summary is the decision state a card needs without loading the feed.
type Summary struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	At    string `json:"at"`
}

// Settings holds the room's effective approval durations.
type Settings struct {
	Wait   time.Duration `json:"wait"`
	Expiry time.Duration `json:"expiry"`
}

// SettingsInput reads or changes owner-only room settings.
type SettingsInput struct {
	Room   string        `json:"room"`
	Wait   time.Duration `json:"wait,omitempty"`
	Expiry time.Duration `json:"expiry,omitempty"`
	Set    bool          `json:"set,omitempty"`
}
