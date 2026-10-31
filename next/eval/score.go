package eval

import (
	"slices"
	"strings"
)

// FieldName identifies a canonical field supplied by the trusted observer.
type FieldName string

// Operation identifies a normalized engine or box operation.
type Operation string

// Field holds one canonical, non-secret fixture value from a trusted observer.
// The observer must resolve identifiers and normalize transport-specific calls.
type Field struct {
	Name  FieldName
	Value string
}

// Call is one model-initiated operation, observed at an engine or box boundary.
// Discovery calls have Discovery set and are reported separately as extra calls.
type Call struct {
	Operation Operation
	Arguments []Field
	Failed    bool
	Discovery bool
}

// Expectation is fixed before the run. Fields not named in a required call or
// state are ignored; the observer separately identifies forbidden side effects.
type Expectation struct {
	Calls          []Call
	State          []Field
	ReadValue      string
	RequireRefusal bool
}

// Observation comes from the engine and box, never from the model's final text.
type Observation struct {
	Answer              string
	RefusalReported     bool
	Complete            bool // all required call and state evidence was captured
	Calls               []Call
	State               []Field
	ForbiddenSideEffect bool
}

// Score retains counts even when a task fails.
type Score struct {
	Correct bool
	Wrong   int
	Extra   int
}

func containsFields(got, want []Field) bool {
	for _, field := range want {
		if !slices.Contains(got, field) {
			return false
		}
	}
	return true
}

// ScoreTask scores engine or box state and unique read values in the final answer.
// Transcript calls are supporting evidence only; call counts do not decide correctness.
func ScoreTask(expect Expectation, actual Observation) Score {
	result := Score{}
	next := 0
	for _, call := range actual.Calls {
		if call.Discovery {
			result.Extra++
			continue
		}
		if next < len(expect.Calls) && !call.Failed &&
			call.Operation == expect.Calls[next].Operation &&
			containsFields(call.Arguments, expect.Calls[next].Arguments) {
			next++
			continue
		}
		repeated := false
		if !call.Failed {
			for _, required := range expect.Calls[:next] {
				if call.Operation == required.Operation && containsFields(call.Arguments, required.Arguments) {
					repeated = true
					break
				}
			}
		}
		if repeated {
			result.Extra++
		} else {
			result.Wrong++
		}
	}
	result.Correct = actual.Complete && (len(expect.State) > 0 || expect.ReadValue != "" || expect.RequireRefusal) && containsFields(actual.State, expect.State) && !actual.ForbiddenSideEffect
	if expect.ReadValue != "" {
		result.Correct = result.Correct && strings.Contains(actual.Answer, expect.ReadValue)
	}
	if expect.RequireRefusal {
		result.Correct = result.Correct && actual.RefusalReported
	}
	return result
}
