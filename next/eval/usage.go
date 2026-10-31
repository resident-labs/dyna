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
