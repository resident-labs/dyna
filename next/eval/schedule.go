package eval

// Provider identifies the live trial route, not a copied credential.
type Provider string

const Claude Provider = "claude"

// Arm selects the profile's agentSurface setting.
type Arm string

const (
	Caller   Arm = "caller"
	Declared Arm = "declared"
)

type Cell struct {
	Task     int
	Provider Provider
	Arm      Arm
}

// Schedule runs one complete pass per shape. Approval and host-access tasks are
// always skipped. Repository listing is skipped when setup would need a fetch.
func Schedule(skipRepository bool) []Cell {
	var cells []Cell
	for _, arm := range []Arm{Caller, Declared} {
		for _, task := range tasks {
			if task.ID == 14 || task.ID == 15 || task.ID == 16 || (task.ID == 2 && skipRepository) {
				continue
			}
			cells = append(cells, Cell{Task: task.ID, Provider: Claude, Arm: arm})
		}
	}
	return cells
}

// RetryAllowed permits one immediate retry only before any model output.
func RetryAllowed(previousAttempts int, providerOrProxyFailure, modelProducedOutput bool) bool {
	return previousAttempts == 1 && providerOrProxyFailure && !modelProducedOutput
}
