package harness

import (
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

type anchorInput struct{}

func (anchorInput) Write(p []byte) (int, error) { return len(p), nil }
func (anchorInput) Close() error                { return nil }

func TestAcknowledgementCapturesBoundaryBeforeNextOutput(t *testing.T) {
	s := newStreamInput(anchorInput{})
	offset, captures := int64(42), 0
	s.captureBoundary = func() *int64 {
		captures++
		at := offset
		return &at
	}
	message := runstore.SteeringMessage{ID: "msg_anchor", Message: "continue"}
	if !s.write(streamItemSteering, "continue", []runstore.SteeringMessage{message}, 0) {
		t.Fatal("write failed")
	}
	acknowledged := make(chan struct{})
	advanced := make(chan struct{})
	go func() {
		s.observe(replayLine("continue"), 0, workeroutput.Usage{})
		close(acknowledged)
		<-advanced
		s.observe(replayLine("continue"), 0, workeroutput.Usage{})
		close(advanced)
	}()
	<-acknowledged
	offset = 99
	advanced <- struct{}{}
	<-advanced
	delivered := 0
	turn := streamTurn{stream: s, steering: &steeringSupervisor{opts: SteeringOptions{
		OnMessage: func(got runstore.SteeringMessage, at *int64) {
			delivered++
			if got.ID != message.ID || at == nil || *at != 42 {
				t.Fatalf("delivery = %v at %v; want msg_anchor at 42", got, at)
			}
		},
	}}}
	if _, _, err := turn.handle(nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := turn.handle(nil); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 || captures != 1 {
		t.Fatalf("deliveries = %d, captures = %d; want one each", delivered, captures)
	}
}
