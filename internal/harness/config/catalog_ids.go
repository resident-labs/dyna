package config

// CanonicalHarnessID returns the ID shared by catalog and runtime adapters.
// TODO: legacy/shim remove later — accepts old Claude and Antigravity IDs until saved catalogs and callers use canonical IDs.
func CanonicalHarnessID(h string) string {
	switch h {
	case "claude":
		return "claude-code"
	case "agy":
		return "antigravity"
	}
	return h
}
