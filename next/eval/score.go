package eval

import "slices"

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
	Calls []Call
	State []Field
}

// Observation comes from the engine and box, never from the model's final text.
type Observation struct {
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

// ScoreTask matches the required call sequence and the observed state.
// A wrong or failed call cannot satisfy a requirement. Discovery and redundant
// successful calls count as extra. Other calls count as wrong.
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
	result.Correct = actual.Complete && len(expect.Calls) > 0 && next == len(expect.Calls) && containsFields(actual.State, expect.State) && !actual.ForbiddenSideEffect
	return result
}
