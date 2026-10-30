package workeroutput

// Usage is token accounting reported by a harness for one turn.
type Usage struct {
	InputTokens  int64   `json:"inputTokens,omitempty"`
	CachedTokens int64   `json:"cachedTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

// UsageNormalization describes observed coverage, not billing verification.
// Absent metadata identifies legacy records whose counters have not been normalized.
type UsageNormalization struct {
	Version     int    `json:"version"`
	Source      string `json:"source"`
	Input       string `json:"input"`
	CachedInput string `json:"cachedInput"`
	Output      string `json:"output"`
	Cost        string `json:"cost"`
	// CostBasis is CostBasisTurn when Cost holds this turn's own spend. Claude
	// turns saved before it was set hold the session's cumulative total.
	CostBasis string `json:"costBasis,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// CallUsage is the prompt accounting of one model call. A nil field was not
// reported, so an unreported count never reads as zero. Context is the prompt
// size, the input plus cache reads and writes, and is nil unless all three are
// reported.
type CallUsage struct {
	Input      *int64 `json:"inputTokens,omitempty"`
	CacheRead  *int64 `json:"cacheReadTokens,omitempty"`
	CacheWrite *int64 `json:"cacheWriteTokens,omitempty"`
	Context    *int64 `json:"contextTokens,omitempty"`
}
