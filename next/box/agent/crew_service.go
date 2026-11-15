package agent

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const (
	// handlerSlots is how many events a service's box handles at once. The rest wait in the engine's queue, where latest still
	// replaces them; the workers of the handlers share the box's slots.
	handlerSlots = 32
	// eventPoll is how long an idle service waits before it asks the engine for events again.
	eventPoll = 2 * time.Second
	// ackRetry is how long an acknowledgement waits after a failed try. The row stays claimed until it lands.
	ackRetry = 2 * time.Second
)

// serve hands a service's events to handlers until run's VM ends, with a reload or the end of the run. Each event gets a VM of
// its own that runs the script again and calls the handler. An event whose handler a reload cut off is run again with the new
// version: its steps are keyed by its row, so the finished ones return at once and the running ones are joined.
func (c *Crew) serve(work context.Context, run *execution, workflow contract.BoxWorkflow) {
	var handlers sync.WaitGroup
	defer handlers.Wait()
	wake := make(chan struct{}, 1)
	handle := func(event contract.WorkflowEvent) {
		handlers.Go(func() {
			if c.handler(work, run, workflow, event) {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		})
	}
	c.mu.Lock()
	cut := make([]contract.WorkflowEvent, 0, len(c.held))
	for _, event := range c.held {
		cut = append(cut, event)
	}
	boot := c.boot
	c.mu.Unlock()
	for _, event := range cut {
		handle(event)
	}
	for {
		c.mu.Lock()
		free := handlerSlots - len(c.held)
		c.mu.Unlock()
		if free > 0 {
			response, err := c.config.Engine.WorkflowEvents(run.ctx, contract.WorkflowEventsRequest{Boot: boot, Limit: free})
			if err != nil && run.ctx.Err() == nil {
				log.Printf("workflow events: %v", err)
			}
			c.mu.Lock()
			for _, event := range response.Events {
				c.held[event.Row] = event
			}
			c.mu.Unlock()
			for _, event := range response.Events {
				handle(event)
			}
			if len(response.Events) > 0 {
				continue
			}
		}
		select {
		case <-time.After(eventPoll):
		case <-wake:
		case <-run.ctx.Done():
			return
		}
	}
}

// handler runs the script for one event and acknowledges the row when the handler ends, whether it returned or threw. A VM
// the run stopped acknowledges nothing: the row stays held for the next version, or for the next box. It reports whether the
// row was acknowledged.
func (c *Crew) handler(work context.Context, parent *execution, workflow contract.BoxWorkflow, event contract.WorkflowEvent) bool {
	ctx, cancel := context.WithCancel(parent.ctx)
	defer cancel()
	run := &execution{version: workflow.Version, seen: make(map[string]int), ctx: ctx, steps: parent.steps}
	args := workflow.Args
	if args == "" {
		args = "null"
	}
	end, err := c.runScript(work, run, workflow.Script, args, "", 0, &serviceMode{Scope: "boot:" + c.boot, Handler: &handlerEvent{Row: event.Row, Key: event.Key, Event: event.Event}})
	if ctx.Err() != nil {
		return false
	}
	done := contract.WorkflowEventDoneRequest{Row: event.Row}
	switch {
	case err != nil:
		done.Error = err.Error()
	case end.Failure != nil:
		done.Error = end.Failure.Reason
	}
	for {
		if err := c.config.Engine.WorkflowEventDone(work, done); err == nil {
			break
		}
		select {
		case <-time.After(ackRetry):
		case <-work.Done():
			return false
		}
	}
	c.mu.Lock()
	delete(c.held, event.Row)
	c.mu.Unlock()
	return true
}
