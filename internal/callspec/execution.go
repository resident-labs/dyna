package callspec

import "errors"

// ExecutionRef names a session turn or a workflow agent. AdmissionID identifies
// one native process attempt and may be empty before admission.
type ExecutionRef struct {
	Session     string `json:"session,omitempty"`
	Turn        int    `json:"turn,omitempty"`
	Run         string `json:"run,omitempty"`
	Agent       int    `json:"agent,omitempty"`
	AdmissionID string `json:"admissionId,omitempty"`
}

// Validate checks that the reference names exactly one execution parent.
func (ref ExecutionRef) Validate() error {
	if ref.Session != "" && ref.Turn > 0 && ref.Run == "" && ref.Agent == 0 {
		return nil
	}
	if ref.Run != "" && ref.Agent > 0 && ref.Session == "" && ref.Turn == 0 {
		return nil
	}
	return errors.New("execution reference needs a session and positive turn or a run and positive agent")
}
