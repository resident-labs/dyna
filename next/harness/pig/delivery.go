package pig

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"

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

// deliveries is what the session file has been seen to record, and how far it was read. Every input asks Delivered at least
// twice, and the file only grows, so each check reads what was appended since the last one and not the whole history: on a
// 588 MB session the whole file took four seconds per check, and a turn's inputs kept the box agent's one CPU busy for minutes.
type deliveries struct {
	mu   sync.Mutex
	path string
	read int64 // bytes of complete lines already read
	seen map[string]struct{}
}

// Delivered reports whether the session file records the delivery: an input is done only once it is there.
func (h *Harness) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	path := h.session.Path()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	d := &h.delivered
	d.mu.Lock()
	defer d.mu.Unlock()
	// Another file, or one that shrank, is read from its start.
	if d.path != path || info.Size() < d.read {
		d.path, d.read, d.seen = path, 0, make(map[string]struct{})
	}
	if _, err := file.Seek(d.read, io.SeekStart); err != nil {
		return false, err
	}
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		// A line an interrupted write left unfinished is not counted, and is read again once it is whole.
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, err
		}
		d.read += int64(len(line))
		// Only a line that holds the member's name can name a delivery, so the rest are not decoded.
		var entry recorded
		if !bytes.Contains(line, []byte(deliveryMember)) || json.Unmarshal(line, &entry) != nil {
			continue
		}
		if delivery, ok := entry.delivery(); ok {
			d.seen[delivery] = struct{}{}
		}
	}
	_, found := d.seen[strconv.FormatInt(int64(id), 10)]
	return found, nil
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
