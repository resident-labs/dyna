package callspec

import "time"

// RunDirectories adds ownership evidence for one coordinator's run. The engine
// derives authority from the verified caller, not this resource reference.
type RunDirectories struct {
	Run         string   `json:"run"`
	Directories []string `json:"directories"`
}

// RunFinish records a coordinator's terminal outcome. Directory completeness
// stays false if any directory update was refused or not acknowledged.
type RunFinish struct {
	Run                 string    `json:"run"`
	Status              string    `json:"status"`
	Error               string    `json:"error,omitempty"`
	EndedAt             time.Time `json:"endedAt"`
	DirectoriesComplete bool      `json:"directoriesComplete"`
}
