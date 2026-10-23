package callspec

// RunDirectories adds ownership evidence for one coordinator's run. The engine
// derives authority from the verified caller, not this resource reference.
type RunDirectories struct {
	Run         string   `json:"run"`
	Directories []string `json:"directories"`
}
