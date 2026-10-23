package subagent

import "dyna-agent/internal/harness"

func sessionExecution(role string) harness.ExecutionOptions {
	return harness.ExecutionOptions{DisableNativeSubagents: true, DynaShell: true}
}
