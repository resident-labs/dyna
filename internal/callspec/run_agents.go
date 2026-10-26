package callspec

import (
	"encoding/json"
	"time"

	"dyna-agent/internal/profile"
)

// RunAgentWaitBound is how long one WaitRunAgent call waits for an agent's
// turn. The coordinator asks again when the turn is still running, so no call
// is longer than this and an engine restart cuts off at most one wait.
const RunAgentWaitBound = 25 * time.Second

// RunAgentStart starts the leaf session that serves one agent() call of a
// run. The engine derives the session ID from Run and Agent, so a coordinator
// that asks again finds the session it started.
type RunAgentStart struct {
	Run   string `json:"run"`
	Agent int    `json:"agent"`
	// Profile is the registered profile the agent runs on.
	Profile string `json:"profile"`
	// Dir is the agent's working directory: the call's cwd or its worktree.
	Dir  string `json:"dir"`
	Task string `json:"task"`
	// Schema is the optional JSON Schema of the agent's result.
	Schema   string    `json:"schema,omitempty"`
	Deadline time.Time `json:"deadline"`
	// Journal and JournalRoot are the run-owned journal `dyna journal` writes.
	Journal     string `json:"journal,omitempty"`
	JournalRoot string `json:"journalRoot,omitempty"`
	// Inline is a mock-harness profile that only this run defines (the demo's).
	// The engine refuses any other harness here.
	Inline *profile.Profile `json:"inline,omitempty"`
}

// RunAgentRef names one agent call of a run.
type RunAgentRef struct {
	Run   string `json:"run"`
	Agent int    `json:"agent"`
}

// RunAgentState is the answer to one WaitRunAgent call. A turn that is not
// Done is still running. OK is true when the turn ended with a result; an
// ended turn that is not OK failed or was stopped, and Error says why.
type RunAgentState struct {
	Done   bool            `json:"done"`
	OK     bool            `json:"ok,omitempty"`
	Status string          `json:"status,omitempty"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}
