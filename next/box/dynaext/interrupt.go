package dynaext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/builtin/codemode"
	"github.com/resident-labs/dyna/contract"
)

// Interrupted is the cause of a script stopped for an input from above.
type Interrupted struct{ Sender contract.Interrupt }

func (i Interrupted) Error() string { return i.Sender.Sender() + " sent you a message" }

type scriptWait struct {
	cancel context.CancelCauseFunc
	timer  *time.Timer
}

// Interrupts owns only the executions currently inside codemode. It keeps no
// sticky signal: the next script starts with a new subscription.
type Interrupts struct {
	mu      sync.Mutex
	scripts map[*scriptWait]struct{}
	grace   time.Duration
}

func NewInterrupts() *Interrupts {
	return &Interrupts{scripts: map[*scriptWait]struct{}{}, grace: 5 * time.Second}
}
func (i *Interrupts) Send(sender contract.Interrupt) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for script := range i.scripts {
		if script.timer == nil {
			script.timer = time.AfterFunc(i.grace, func() {
				i.mu.Lock()
				defer i.mu.Unlock()
				if _, ok := i.scripts[script]; ok {
					script.cancel(Interrupted{Sender: sender})
				}
			})
		}
	}
}

func codemodeTool(interrupts *Interrupts, settings func() extension.Settings) extension.ToolDefinition {
	definition := codemode.Definition(codemode.Options{Models: true, GetSettings: settings})
	// PrepareLoadout regenerates the description, so extend it there as well.
	const note = " A message from the owner or an agent above gives a running script 5 seconds to finish, then stops it with output so far and a call summary; its store writes are not saved. Background bash jobs keep running."
	definition.Description += note
	prepare := definition.PrepareLoadout
	definition.PrepareLoadout = func(loadout extension.ToolLoadout) *extension.ToolLoadoutChanges {
		changes := prepare(loadout)
		if changes != nil {
			if text, ok := changes.Descriptions["codemode"]; ok {
				changes.Descriptions["codemode"] = text + note
			}
		}
		return changes
	}
	execute := definition.Execute
	definition.Execute = func(ctx context.Context, id string, params json.RawMessage, onUpdate extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
		ctx, cancel := context.WithCancelCause(ctx)
		script := &scriptWait{cancel: cancel}
		interrupts.mu.Lock()
		interrupts.scripts[script] = struct{}{}
		interrupts.mu.Unlock()
		defer func() {
			interrupts.mu.Lock()
			delete(interrupts.scripts, script)
			if script.timer != nil {
				script.timer.Stop()
			}
			interrupts.mu.Unlock()
			cancel(nil)
		}()
		result, err := execute(ctx, id, params, onUpdate)
		var interrupted Interrupted
		if err != nil || !result.IsError || !errors.As(context.Cause(ctx), &interrupted) {
			return result, err
		}
		details := result.Details.(codemode.ToolDetails)
		// PiG cancellation already discards store writes and returns bounded partial
		// output. Keep its details for the transcript and also show the model a summary.
		summary := make([]string, 0, 9)
		for n, call := range details.Calls {
			if n == 8 {
				summary = append(summary, fmt.Sprintf("%d more calls in the transcript summary", len(details.Calls)-n))
				break
			}
			state := "done"
			if call.Status == codemode.StatusCancelled {
				state = "cancelled"
			} else if call.Status == codemode.StatusError {
				state = "failed"
			}
			summary = append(summary, fmt.Sprintf("%s: %s", call.Name, state))
		}
		for n, item := range result.Content {
			if text, ok := item.(ai.TextContent); ok {
				text.Text = strings.Replace(text.Text, "Script failed", "Script stopped", 1)
				result.Content[n] = text
				break
			}
		}
		result.Content = append(result.Content, ai.TextContent{Text: fmt.Sprintf("Calls (%d):\n%s\n\n%s sent you a message, so this script stopped after %d calls. Calls listed as done ran; cancelled ones did not finish. Background jobs it started keep running and wake you when they end. The script's store writes were not saved. Read the message, then rerun what is still needed.", len(details.Calls), strings.Join(summary, "\n"), interrupted.Sender.Sender(), len(details.Calls))})
		return result, nil
	}
	return definition
}
