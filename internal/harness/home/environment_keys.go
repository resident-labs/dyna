package home

// PrivateEnvironmentKey reports selectors owned by private harness launches.
// MCP references use aliases for these names, then restore them only in servers.
func PrivateEnvironmentKey(key string) bool { return privateEnvironmentKeys[key] }

var privateEnvironmentKeys = func() map[string]bool {
	keys := map[string]bool{
		"PATH":              true,
		"GIT_CONFIG_GLOBAL": true, "GH_CONFIG_DIR": true, "NODE_COMPILE_CACHE": true,
		"DYNA_BIN": true, "DYNA_HOME_TOOLS": true, OwnerEnvKey: true,
	}
	for key := range Roots("/private-scope") {
		keys[key] = true
	}
	for key := range Discovery("/private-scope") {
		keys[key] = true
	}
	// Explicit Go values avoid process lookup. This reads the actual tool-root
	// key set without consulting HOME, the current environment, or executables.
	tools, _ := ToolRoots([]string{"HOME=/owner", "PATH=", "GOPATH=", "GOMODCACHE=", "GOCACHE="})
	for key := range tools {
		keys[key] = true
	}
	return keys
}()
