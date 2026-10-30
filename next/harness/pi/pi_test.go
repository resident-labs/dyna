package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The fixture contains selected, sanitized events from Pi 1.4.0-dyna.10,
// session ses_20261030-143118-fad2075a, turn 7.
func TestRecordedTurnAndSteer(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &Harness{
		stdin: inputWriter, pending: make(map[string]chan event),
		events: make(chan event, 256), done: make(chan struct{}), stop: make(chan struct{}),
	}
	go h.read(outputReader)
	defer inputWriter.Close()
	defer outputWriter.Close()
	reader := bufio.NewReader(inputReader)
	result := make(chan contract.TurnEnded, 1)
	errs := make(chan error, 1)
	go func() {
		r, err := h.Start(context.Background(), "turn_test", contract.TurnInput{Delivery: 21, Text: "start"})
		if err != nil {
			errs <- err
			return
		}
		result <- r
	}()
	readCommand := func() command {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var c command
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	respond := func(c command) {
		t.Helper()
		if err := json.NewEncoder(outputWriter).Encode(event{ID: c.ID, Type: "response", Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	c := readCommand()
	if c.Type != "prompt" || c.DeliveryID != "21" {
		t.Fatalf("prompt: %+v", c)
	}
	respond(c)
	steered := make(chan error, 1)
	go func() { steered <- h.Steer(context.Background(), contract.TurnInput{Delivery: 22, Text: "steer"}) }()
	c = readCommand()
	if c.Type != "steer" || c.DeliveryID != "22" || c.Message != "steer" {
		t.Fatalf("steer: %+v", c)
	}
	respond(c)
	if err := <-steered; err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/p4-turn7.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outputWriter.Write(fixture); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	case got := <-result:
		if got.Outcome != contract.TurnOutcome("completed") || !strings.HasPrefix(got.Final, "PR [#1289]") {
			t.Fatalf("unexpected result: %+v", got)
		}
		if got.Usage.Model != "gpt-6.1-terra" || got.Usage.InputTokens != 17936 || got.Usage.OutputTokens != 212 || got.Usage.CacheReadTokens != 26496 {
			t.Fatalf("unexpected usage: %+v", got.Usage)
		}
	}
}
