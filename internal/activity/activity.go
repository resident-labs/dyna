// Package activity defines observations shared by managed work producers.
package activity

import "time"

// CopierState describes the output observer, not the work itself.
type CopierState string

const (
	CopierUnknown CopierState = ""
	CopierRunning CopierState = "running"
	CopierEnded   CopierState = "ended"

	// PublishInterval bounds the rate of state writes.
	PublishInterval = time.Second
	// Lease bounds how long a published observation can be trusted.
	Lease = 3 * PublishInterval
)

// CPUState describes whether command-only CPU accounting is available.
// Unsupported is not evidence of zero CPU use.
type CPUState string

const (
	CPUUnknown     CPUState = ""
	CPUUnsupported CPUState = "unsupported"
)

// Signal separates output activity from the state of its observer.
type Signal struct {
	LastOutput time.Time
	LeaseUntil time.Time
	Copier     CopierState
	CPU        CPUState
}

// At returns unknown when the observation's lease is absent or expired.
func (s Signal) At(now time.Time) Signal {
	if s.LeaseUntil.IsZero() || !now.Before(s.LeaseUntil) {
		s.Copier = CopierUnknown
		s.CPU = CPUUnknown
	}
	return s
}
