package adapters

// Effort is one reasoning level a model accepts. Args select it on the
// harness command line, so a profile stores them in extraArgs. Harnesses that
// encode the level in the model ID set Model instead, and the profile uses
// that ID.
type Effort struct {
	ID      string   `json:"id"`
	Label   string   `json:"label,omitempty"`
	Default bool     `json:"default,omitempty"`
	Args    []string `json:"args" tstype:"string[] | null"`
	Model   string   `json:"model,omitempty"`
}
