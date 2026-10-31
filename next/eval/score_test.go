package eval

import (
	"testing"
)

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

func TestReadScoreUsesUniqueAnswerNotCalls(t *testing.T) {
	want := Expectation{ReadValue: "invented-4f31"}
	got := ScoreTask(want, Observation{Complete: true, Answer: "invented-4f31"})
	if !got.Correct {
		t.Fatal("unique value was not credited")
	}
	got = ScoreTask(want, Observation{Complete: true, Calls: []Call{{Operation: "events.list"}}, Answer: "not found"})
	if got.Correct {
		t.Fatal("calls alone passed a read")
	}
}

func TestFrozenReplacementOutcomes(t *testing.T) {
	f := Fixture{A: "child", AName: "child-name", File: "/seed/file", FileSHA256: "sha", ReadValue: "unique-code"}
	restart, _ := Expected(17, f)
	if !ScoreTask(restart, Observation{Complete: true, State: []Field{field("child.box.replaced", "true"), field("child.state", "idle")}}).Correct {
		t.Fatal("restart state not credited")
	}
	attachment, _ := Expected(18, f)
	state := []Field{field("posts", "1"), field("recipient", f.A), field("text", "Results attached."), field("attachments", "1"), field("attachment.sha256", f.FileSHA256)}
	if !ScoreTask(attachment, Observation{Complete: true, State: state}).Correct {
		t.Fatal("attachment state not credited")
	}
	state[4].Value = "wrong"
	if ScoreTask(attachment, Observation{Complete: true, State: state}).Correct {
		t.Fatal("wrong attachment passed")
	}
	reveal, _ := Expected(20, f)
	if !ScoreTask(reveal, Observation{Complete: true, Answer: "unique-code"}).Correct {
		t.Fatal("reveal code not credited")
	}

}

func TestFixtureValidationRequiresOnlyTaskInputs(t *testing.T) {
	f := Fixture{Room: "room", ReadValue: "unique"}
	if err := f.Validate(1); err != nil {
		t.Fatal(err)
	}
	f.ReadValue = ""
	if err := f.Validate(1); err == nil {
		t.Fatal("missing read value accepted")
	}
	if err := (Fixture{A: "child"}).Validate(17); err != nil {
		t.Fatal(err)
	}
}
