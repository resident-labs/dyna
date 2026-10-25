package config

// CanonicalHarnessID returns the ID shared by catalog and runtime adapters.
// TODO: legacy/shim remove later — accepts the old Claude ID until saved catalogs and callers use the canonical ID.
func CanonicalHarnessID(h string) string {
	if h == "claude" {
		return "claude-code"
	}
	return h
}
