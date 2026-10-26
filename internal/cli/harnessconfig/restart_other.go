//go:build !linux

package harnessconfig

// restartOnOutdated has no restart outside Linux: the platforms without
// /proc/self/exe cannot name the binary an update replaced, so an outdated
// server keeps reporting the schema error the way it did before (D-171).
func restartOnOutdated([]string) func() { return nil }
