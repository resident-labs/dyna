//go:build !linux

package shells

// stopOrphans finds nothing where no process environment can be read.
func stopOrphans(id string) (bool, error) { return false, nil }
