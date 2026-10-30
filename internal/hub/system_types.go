package hub

// Stats is the hub's part of the engine's Health call.
type Stats struct {
	Seq     uint64 `json:"seq"`     // newest event rev
	Streams int    `json:"streams"` // open subscriptions
	MaxLag  int    `json:"maxLag"`  // most events waiting in one subscription
	Dropped int    `json:"dropped"` // subscriptions ended for being too slow
}
