package eval

// Usage holds disjoint token categories normalized by the provider adapter.
type Usage struct {
	Input      int64 // uncached input
	CacheRead  int64
	CacheWrite int64
	Output     int64
}

// Total counts each token once, without price weights.
func (u Usage) Total() int64 { return u.Input + u.CacheRead + u.CacheWrite + u.Output }

// Run is a single task result. Missing usage or an infrastructure failure
// before any model output makes the run unmeasured.
type Run struct {
	Task     int
	Provider Provider
	Arm      Arm
	Measured bool
	Correct  bool
	Usage    Usage
}

// Gate compares twenty matched tasks. Unmeasured results cannot pass.
type Gate struct {
	Measured   bool
	Pass       bool
	CLICorrect int
	MCPCorrect int
	CLITokens  int64
	MCPTokens  int64
}

// Compare rejects duplicates, missing tasks and unavailable token data. The
// per-task mean has the same ratio as the sum for equally sized arms.
func Compare(cli, mcp []Run) Gate {
	result := Gate{}
	if len(cli) != len(tasks) || len(mcp) != len(tasks) {
		return result
	}
	provider, mcpArm := cli[0].Provider, mcp[0].Arm
	if (provider != Opus && provider != Sonnet && provider != ChatGPT && provider != Zai && provider != Kimi) || cli[0].Arm != Off || (mcpArm != Caller && mcpArm != Declared) {
		return result
	}
	var cliSeen, mcpSeen [20]bool
	for _, arm := range []struct {
		runs    []Run
		arm     Arm
		seen    *[20]bool
		correct *int
		tokens  *int64
	}{{cli, Off, &cliSeen, &result.CLICorrect, &result.CLITokens}, {mcp, mcpArm, &mcpSeen, &result.MCPCorrect, &result.MCPTokens}} {
		for _, run := range arm.runs {
			if !run.Measured || run.Provider != provider || run.Arm != arm.arm || run.Task < 1 || run.Task > len(tasks) || arm.seen[run.Task-1] ||
				run.Usage.Input < 0 || run.Usage.CacheRead < 0 || run.Usage.CacheWrite < 0 || run.Usage.Output < 0 {
				return Gate{}
			}
			arm.seen[run.Task-1] = true
			if run.Correct {
				*arm.correct++
			}
			*arm.tokens += run.Usage.Total()
		}
	}
	if result.CLITokens <= 0 || result.MCPTokens <= 0 {
		return Gate{}
	}
	result.Measured = true
	result.Pass = result.MCPCorrect >= result.CLICorrect && float64(result.MCPTokens) <= float64(result.CLITokens)*0.8
	return result
}
