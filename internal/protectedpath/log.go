// Package protectedpath reports retained cleanup paths once per process.
package protectedpath

import (
	"log"
	"path/filepath"
	"sync"
)

var logged sync.Map

// LogOnce reports the first cleanup protection notice for path.
// A nil report uses the standard process logger.
func LogOnce(path string, report func(string, ...any), format string, args ...any) {
	if _, exists := logged.LoadOrStore(filepath.Clean(path), struct{}{}); exists {
		return
	}
	if report == nil {
		report = log.Printf
	}
	report(format, args...)
}
