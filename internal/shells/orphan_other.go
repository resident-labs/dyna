//go:build !linux

package shells

import "time"

// stopOrphans finds nothing where no process environment can be read.
func stopOrphans(id string, grace time.Duration) (bool, error) { return false, nil }
