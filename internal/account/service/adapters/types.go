// Package adapters isolates native harness discovery and account mutations.
// Results contain projections only; credentials and native raw output never escape.
package adapters

import "time"

type Account struct {
	Status      string    `json:"status,omitempty"`
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Email       string    `json:"email,omitempty"`
	Integration string    `json:"integration,omitempty"`
	Active      bool      `json:"active"`
	Switchable  bool      `json:"switchable"`
	Verified    bool      `json:"verified"`
	Plan        string    `json:"plan,omitempty"`
	Windows     []Window  `json:"windows,omitempty"`
	ObservedAt  time.Time `json:"observedAt,omitempty"`
}
type Window struct {
	Period      string   `json:"period,omitempty"`
	PeriodStart string   `json:"periodStart,omitempty"`
	AccountID   string   `json:"accountId,omitempty"`
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	UsedPercent *float64 `json:"usedPercent,omitempty"`
	Used        *float64 `json:"used,omitempty"`
	Limit       *float64 `json:"limit,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	ResetsAt    string   `json:"resetsAt,omitempty"`
}
type Snapshot struct {
	Harness    string    `json:"harness"`
	Status     string    `json:"status"` // ready, unavailable, unknown, failed
	Accounts   []Account `json:"accounts"`
	Windows    []Window  `json:"windows"`
	Source     string    `json:"source,omitempty"`
	Message    string    `json:"message,omitempty"`
	SwitchMode string    `json:"switchMode"` // native, helper, unavailable
	Scope      string    `json:"scope,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
}
type Model struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider,omitempty"`
	Integration string   `json:"integration,omitempty"`
	Input       int64    `json:"input,omitempty"`
	Source      string   `json:"source,omitempty"`
	Context     int64    `json:"context,omitempty"`
	Output      int64    `json:"output,omitempty"`
	Description string   `json:"description,omitempty"`
	Efforts     []Effort `json:"efforts,omitempty"`
}

// Effort is one reasoning level a model accepts. Args select it on the
// harness command line, so a profile stores them in extraArgs. Harnesses that
// encode the level in the model ID set Model instead, and the profile uses
// that ID.
type Effort struct {
	ID      string   `json:"id"`
	Label   string   `json:"label,omitempty"`
	Default bool     `json:"default,omitempty"`
	Args    []string `json:"args"`
	Model   string   `json:"model,omitempty"`
}
type Target struct {
	Harness     string `json:"harness"`
	Executable  string `json:"executable"`
	AccountID   string `json:"accountId"`
	Integration string `json:"integration,omitempty"`
}
type SwitchResult struct {
	Verified  bool   `json:"verified"`
	AccountID string `json:"accountId"`
	Scope     string `json:"scope"`
	Message   string `json:"message"`
}
