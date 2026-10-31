package eval

// Provider is an exact model route, not a fallback provider.
type Provider string

const (
	Opus    Provider = "claude-opus"
	Sonnet  Provider = "claude-sonnet"
	ChatGPT Provider = "chatgpt"
	Zai     Provider = "zai"
	Kimi    Provider = "kimi"
)

// Arm selects the profile's agentSurface setting.
type Arm string

const (
	Off      Arm = "off"
	Caller   Arm = "caller"
	Declared Arm = "declared"
)

// Cell is one fixed task for one provider and arm.
type Cell struct {
	Task     int
	Provider Provider
	Arm      Arm
}

// Schedule rotates the provider/arm order by task. It is deterministic and
// contains exactly one cell for every provider, arm and task.
func Schedule() []Cell {
	providers := [...]Provider{Opus, Sonnet, ChatGPT, Zai, Kimi}
	arms := [...]Arm{Off, Caller, Declared}
	var pairs []Cell
	for _, p := range providers {
		for _, a := range arms {
			pairs = append(pairs, Cell{Provider: p, Arm: a})
		}
	}
	cells := make([]Cell, 0, len(tasks)*len(pairs))
	for _, task := range tasks {
		for i := range pairs {
			cell := pairs[(i+task.ID-1)%len(pairs)]
			cell.Task = task.ID
			cells = append(cells, cell)
		}
	}
	return cells
}

// RetryAllowed permits only the lead-approved immediate retry. It must not be
// used for a model that produced any output, even if the task then fails.
func RetryAllowed(previousAttempts int, providerOrProxyFailure, modelProducedOutput bool) bool {
	return previousAttempts == 1 && providerOrProxyFailure && !modelProducedOutput
}
