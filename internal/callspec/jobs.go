package callspec

import "time"

// JobWaitBound is how long one WaitJob call waits for committed job events.
// The workflow starts another event wait when this one returns without a final job.
const JobWaitBound = 25 * time.Second

// RunJobInput is the RunJob call's argument. The engine derives room, session
// and workflow ownership from the authenticated caller, never from this input.
type RunJobInput struct {
	// IdempotencyKey returns the original job when a workflow retries a call.
	// Empty gives an ordinary CLI submission no deduplication.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// RequestHash rejects reuse of one key with a different typed request.
	RequestHash string   `json:"requestHash,omitempty"`
	Node        string   `json:"node"`
	Labels      []string `json:"labels"`
	Command     []string `json:"command"`
	Repo        string   `json:"repo"`
	Ref         string   `json:"ref"`
	Host        bool     `json:"host"`
	Image       string   `json:"image"`
	CPUs        int      `json:"cpus"`
	Memory      string   `json:"memory"`
	Timeout     int      `json:"timeoutSec"`
	Idle        *int     `json:"idleSec"`
	// Artifacts are paths of the job's work folder to keep after it ends.
	Artifacts []string `json:"artifacts"`
	// IO is heavy or light: an IO-heavy job shares a node with only a few
	// other IO-heavy ones.
	IO string `json:"io"`
	// Resources are named units the job holds for its whole run.
	Resources []string `json:"resources"`
	Workspace bool     `json:"workspace"`
}
