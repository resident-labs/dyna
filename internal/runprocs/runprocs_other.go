//go:build !linux

// Package runprocs ends the process groups a workflow run started (D-185).
package runprocs

// Kill does nothing where /proc is not available.
func Kill(runID string, leader int, keep map[int]bool) error { return nil }
