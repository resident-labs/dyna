package eval

import (
	"context"
	"testing"
)

type behaviorStub struct {
	steps   []Change
	prompts []string
}

func (*behaviorStub) BeginBehavior(context.Context, Provider, Arm) error { return nil }
func (b *behaviorStub) ApplyAndProbe(_ context.Context, _ Provider, _ Arm, change Change, prompt string) (BehaviorObservation, error) {
	b.steps = append(b.steps, change)
	b.prompts = append(b.prompts, prompt)
	return BehaviorObservation{Change: change}, nil
}
func (*behaviorStub) EndBehavior(context.Context, Provider, Arm) error { return nil }

func TestBehaviorOrderAndProbeAreFixed(t *testing.T) {
	b := &behaviorStub{}
	count := 0
	err := RunBehavior(context.Background(), b, func(_ Provider, _ Arm, _ BehaviorObservation) error { count++; return nil })
	if err != nil || count != 105 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	for i, step := range b.steps {
		if step != behaviorChanges[i%len(behaviorChanges)] || b.prompts[i] != ProbePrompt {
			t.Fatalf("wrong step %d: %q", i, step)
		}
	}
}
