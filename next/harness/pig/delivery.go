package pig

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"

	"github.com/resident-labs/dyna/contract"
)

// deliveryMember is the member of a user message that names the delivery it carries, as Pi's fork names it.
const deliveryMember = "deliveryId"

// members labels an input's user message with its delivery.
func members(input contract.TurnInput) map[string]json.RawMessage {
	return map[string]json.RawMessage{deliveryMember: json.RawMessage(strconv.Quote(strconv.FormatInt(int64(input.Delivery), 10)))}
}

// recorded is one line of a session file, as far as delivery and recovery read it.
type recorded struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

// delivery is the delivery a recorded message carries, if it is a user message that names one.
func (r recorded) delivery() (string, bool) {
	var m struct {
		Role       string `json:"role"`
		DeliveryID string `json:"deliveryId"`
	}
	if r.Type != "message" || json.Unmarshal(r.Message, &m) != nil || m.Role != "user" || m.DeliveryID == "" {
		return "", false
	}
	return m.DeliveryID, true
}

// lines reads a session file's complete lines. A line an interrupted write left unfinished, or one that is not an entry,
// is skipped, as PiG skips it.
func lines(r io.Reader, each func(recorded)) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var entry recorded
		if json.Unmarshal(line, &entry) == nil {
			each(entry)
		}
	}
}

// Delivered reports whether the session file records the delivery: an input is done only once it is there.
func (h *Harness) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	file, err := os.Open(h.session.Path())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	want, found := strconv.FormatInt(int64(id), 10), false
	err = lines(file, func(entry recorded) {
		if delivery, ok := entry.delivery(); ok && delivery == want {
			found = true
		}
	})
	return found, err
}

// Recover finds whether the turn that began with the first input finished before the box agent stopped: a final
// answer, or a tool batch that ended the turn, recorded after that input. Nothing before that input counts, so a file without it
// holds no finished turn.
func (h *Harness) Recover(id contract.TurnID, inputs []contract.TurnInput) (contract.TurnEnded, bool, error) {
	if len(inputs) == 0 {
		return contract.TurnEnded{Turn: id}, false, errors.New("recovery needs at least one input")
	}
	file, err := os.Open(h.session.Path())
	if errors.Is(err, os.ErrNotExist) {
		return contract.TurnEnded{Turn: id}, false, nil
	}
	if err != nil {
		return contract.TurnEnded{Turn: id}, false, err
	}
	defer file.Close()
	return recoverTurn(file, id, inputs[0].Delivery)
}

func recoverTurn(r io.Reader, id contract.TurnID, first contract.DeliverySeq) (contract.TurnEnded, bool, error) {
	t := newTurn(id, func(contract.TurnID, contract.ActivityKind, string) {})
	want := strconv.FormatInt(int64(first), 10)
	found, answered := false, false
	err := lines(r, func(entry recorded) {
		if delivery, ok := entry.delivery(); ok && delivery == want {
			found = true
		}
		var m agent.AgentMessage
		if !found || entry.Type != "message" || json.Unmarshal(entry.Message, &m) != nil {
			return
		}
		t.message(m)
		answered = false
		if m.Assistant != nil && m.Assistant.StopReason == ai.StopReasonStop {
			answered = true
			for _, block := range m.Assistant.Content {
				if _, ok := block.(ai.ToolCall); ok {
					answered = false
				}
			}
		}
	})
	ended := t.batch.apply(&t.result)
	return t.result, answered || ended, err
}
