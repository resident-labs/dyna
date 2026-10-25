// Package health keeps best-effort, per-profile evidence about CLI output.
// It does not run probes, launch commands, or store output or environment values.
package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

type Status string

const (
	Unknown               Status = "unknown"
	Checking              Status = "checking"
	Healthy               Status = "healthy"
	Unhealthy             Status = "unhealthy"
	OutputNotUnderstood   Status = "output_not_understood"
	UnverifiedSinceUpdate Status = "unverified_since_update"
	UnverifiedSinceEdit   Status = "unverified_since_edit"
)

// Observation describes one completed turn or probe, not accumulated evidence.
// At is the completion time; a zero value uses the current time.
// PositiveUsageSeen means reported usage was greater than zero.
type Observation struct {
	Running           bool      `json:"running"`
	At                time.Time `json:"at"`
	Successful        bool      `json:"successful"`
	StdoutSeen        bool      `json:"stdoutSeen"`
	RecognizedRecords int       `json:"recognizedRecords"`
	TextSeen          bool      `json:"textSeen"`
	ToolSeen          bool      `json:"toolSeen"`
	PositiveUsageSeen bool      `json:"positiveUsageSeen"`
}

// Observed contains the latest check and the last ordinary turn time.
type Observed struct {
	Observation
	LastTurnAt       time.Time `json:"lastTurnAt"`
	RecognizedOutput bool      `json:"recognizedOutput"`
	Probe            bool      `json:"probe"`
	ProbePassed      bool      `json:"probePassed"`
}

// View separates the declared contract from observed runtime evidence.
// StorageError is diagnostic only. Callers must never fail a turn on it.
type View struct {
	Declared           workeroutput.DialectCapabilities `json:"declared"`
	Observed           *Observed                        `json:"observed,omitempty"`
	Version            string                           `json:"version"`
	LastHealthyVersion string                           `json:"lastHealthyVersion,omitempty"`
	LastCheckedAt      time.Time                        `json:"lastCheckedAt"`
	Status             Status                           `json:"status"`
	Reason             string                           `json:"reason"`
	StorageError       string                           `json:"storageError,omitempty"`
}

type record struct {
	Fingerprint        string    `json:"fingerprint"`
	Version            string    `json:"version"`
	LastHealthyVersion string    `json:"lastHealthyVersion,omitempty"`
	LastHealthyAt      time.Time `json:"lastHealthyAt"`
	Observed           Observed  `json:"observed"`
}

// Fingerprint changes when the output-relevant profile configuration changes.
// Only its digest is persisted. Env and descriptive profile fields are excluded.
func Fingerprint(p profile.Profile) string {
	b, _ := json.Marshal(struct {
		Harness          string
		Model            string
		ExtraArgs        []string
		Output           *workeroutput.Declaration
		EffectiveDialect string
	}{p.Harness, p.Model, p.ExtraArgs, p.Output, p.EffectiveDialect()})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func baseView(p profile.Profile, version string) View {
	return View{Declared: workeroutput.Capabilities(p.EffectiveDialect()), Version: version,
		Status: Unknown, Reason: "no observed health check"}
}

func makeView(p profile.Profile, version string, r record) View {
	v := baseView(p, version)
	if r.Fingerprint != Fingerprint(p) {
		v.Status, v.Reason = UnverifiedSinceEdit, "profile configuration changed; health is unverified"
		return v
	}
	v.Observed = &r.Observed
	v.LastCheckedAt = r.Observed.At
	v.LastHealthyVersion = r.LastHealthyVersion
	switch {
	case r.Observed.StdoutSeen && !r.Observed.RecognizedOutput:
		v.Status, v.Reason = OutputNotUnderstood, "stdout exists but no output records were recognized"
	case !r.LastHealthyAt.IsZero() && version != r.LastHealthyVersion:
		v.Status, v.Reason = UnverifiedSinceUpdate, fmt.Sprintf("unverified since update to %s", version)
	case r.Version != version:
		v.Status, v.Reason = UnverifiedSinceUpdate, "latest check used a different CLI version"
	case r.Observed.Running:
		v.Status, v.Reason = Checking, "turn is running; observed output is being checked"
	case !r.Observed.Successful:
		v.Status, v.Reason = Unhealthy, "latest turn or probe did not succeed"
	case !r.Observed.RecognizedOutput:
		v.Status, v.Reason = Unhealthy, "latest turn or probe had no recognized output"
	case r.Observed.Probe && !r.Observed.ProbePassed:
		v.Status, v.Reason = Unhealthy, "latest probe did not meet its health criteria"
	default:
		v.Status, v.Reason = Healthy, "successful check with recognized output"
	}
	return v
}
