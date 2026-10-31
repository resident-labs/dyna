package eval

import "testing"

func TestScoreTaskUsesObservedCallsAndState(t *testing.T) {
	want := Expectation{
		Calls: []Call{{Operation: "post", Arguments: []Field{{Name: "recipient", Value: "A"}, {Name: "text", Value: "hello"}}}},
		State: []Field{{Name: "deliveries", Value: "1"}},
	}
	good := Call{Operation: "post", Arguments: []Field{{Name: "recipient", Value: "A"}, {Name: "text", Value: "hello"}}}
	actual := Observation{
		Complete: true,
		Calls:    []Call{{Operation: "tool_search", Discovery: true}, {Operation: "post", Arguments: []Field{{Name: "recipient", Value: "B"}}}, good, good},
		State:    []Field{{Name: "deliveries", Value: "1"}},
	}
	if got := ScoreTask(want, actual); !got.Correct || got.Wrong != 1 || got.Extra != 2 {
		t.Fatalf("got %+v", got)
	}
	actual.State[0].Value = "2"
	if got := ScoreTask(want, actual); got.Correct {
		t.Fatalf("wrong state passed: %+v", got)
	}
	actual.State[0].Value = "1"
	actual.ForbiddenSideEffect = true
	if got := ScoreTask(want, actual); got.Correct {
		t.Fatalf("forbidden side effect passed: %+v", got)
	}
}
