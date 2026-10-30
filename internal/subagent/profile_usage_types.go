package subagent

// ProfileUsageStats is measured activity for one profile. Cost and CachePaired
// retain the rollup's coverage, so partial or unknown reports never become zero.
type ProfileUsageStats struct {
	Profile        string           `json:"profile"`
	Turns          int              `json:"turns"`
	MedianWallMs   *int64           `json:"medianWallMs" tstype:"number | null,required"`
	Cost           UsageCost        `json:"cost" tstype:"Cost"`
	CostPerTurnUSD *float64         `json:"costPerTurnUsd" tstype:"number | null,required"`
	CacheReadShare *float64         `json:"cacheReadShare" tstype:"number | null,required"`
	CachePaired    UsageCachePaired `json:"cachePaired" tstype:"CachePaired"`
}

// ProfileUsageSnapshot answers profile activity for inclusive UTC days.
type ProfileUsageSnapshot struct {
	From     string              `json:"from"`
	To       string              `json:"to"`
	Profiles []ProfileUsageStats `json:"profiles" tstype:"ProfileUsageStats[] | null"`
}
