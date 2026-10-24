package computers

import "os/exec"

// StreamFailureCode is a stable, machine-readable stream setup failure.
type StreamFailureCode string

const (
	StreamWayVNCMissing StreamFailureCode = "wayvnc-missing"
)

// StreamError describes a stream failure without making callers match prose.
type StreamError struct {
	Code             StreamFailureCode
	Detail           string
	PermanentFailure bool
}

func (e *StreamError) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	if e.Code == StreamWayVNCMissing {
		return "the screen streamer (wayvnc) is not installed"
	}
	return "the screen stream is unavailable"
}

// FailureCode returns the stable code carried by the web API.
func (e *StreamError) FailureCode() string { return string(e.Code) }

// Permanent reports whether retrying without changing the machine can succeed.
func (e *StreamError) Permanent() bool { return e.PermanentFailure || e.Code == StreamWayVNCMissing }

// streamFailureResponse preserves a daemon failure across the control protocol.
type streamFailureResponse struct {
	Error     string `json:"error"`
	Reason    string `json:"reason,omitempty"`
	Code      string `json:"code,omitempty"`
	Permanent bool   `json:"permanent,omitempty"`
}

// CheckStreamInstalled checks the one executable every Linux viewer needs.
// Computers still run without it: agents can use the desktop by other means.
func CheckStreamInstalled() error {
	if _, err := exec.LookPath("wayvnc"); err != nil {
		return &StreamError{Code: StreamWayVNCMissing, PermanentFailure: true}
	}
	return nil
}
