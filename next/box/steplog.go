package box

import (
	"log"
	"time"
)

// StepLog logs how long each step of a box start took, so a slow start can be read from the log.
type StepLog struct {
	subject string
	last    time.Time
}

func NewStepLog(subject string) *StepLog { return &StepLog{subject: subject, last: time.Now()} }

// Done logs the time since the previous step ended.
func (s *StepLog) Done(step string) {
	now := time.Now()
	log.Printf("%s: %s took %.3fs", s.subject, step, now.Sub(s.last).Seconds())
	s.last = now
}
