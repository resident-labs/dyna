package eval

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Attempt is observed from a single model turn. ProviderFailure applies only
// to provider or proxy failures; OutputStarted includes tool calls.
type Attempt struct {
	ProviderFailure bool
	OutputStarted   bool
	UsageAvailable  bool
	Usage           Usage
	Observation     Observation
}

// Backend isolates each cell and reads evidence from the engine and box.
// Preflight must fail unless every route, arm, fixture and capture is ready.
// BeginAttempt durably rejects a repeated execution before the model can act.
// Execute performs exactly one attempt; a retry uses the same prepared cell.
type Backend interface {
	Preflight(context.Context) error
	Prepare(context.Context, Cell) (Fixture, error)
	BeginAttempt(context.Context, Cell, int) error
	FinishAttempt(context.Context, Cell, int, Attempt) error
	Execute(context.Context, Cell, Fixture) (Attempt, error)
	Cleanup(context.Context, Cell, Fixture) error
}

// CellResult records every attempt, including the one permitted early retry.
type CellResult struct {
	Cell     Cell
	Attempts []Attempt
	Duration time.Duration
	Score    Score
	Scored   bool
	Measured bool
}

// RunSuite executes the fixed schedule once. Setup, observation and cleanup errors
// stop the run; a repeated early provider failure is retained as unmeasured.
// The backend must journal attempts durably before execution and refuse replays.
// task11Fallback is chosen once from the fake pass and applies to every arm.
func RunSuite(ctx context.Context, backend Backend, task11Fallback bool, emit func(CellResult) error) error {
	if err := backend.Preflight(ctx); err != nil {
		return fmt.Errorf("evaluation preflight: %w", err)
	}
	for _, cell := range Schedule() {
		fixture, err := backend.Prepare(ctx, cell)
		if err != nil {
			return fmt.Errorf("prepare task %d: %w", cell.Task, err)
		}
		fixture.Task11Fallback = task11Fallback
		if err := fixture.Validate(); err != nil {
			_ = backend.Cleanup(ctx, cell, fixture)
			return fmt.Errorf("fixture task %d: %w", cell.Task, err)
		}
		if strings.Contains(tasks[cell.Task-1].PromptFor(fixture), fixture.ReadValue) {
			_ = backend.Cleanup(ctx, cell, fixture)
			return fmt.Errorf("fixture task %d: read value appears in prompt", cell.Task)
		}
		start := time.Now()
		result := CellResult{Cell: cell}
		for {
			if err := backend.BeginAttempt(ctx, cell, len(result.Attempts)+1); err != nil {
				_ = backend.Cleanup(ctx, cell, fixture)
				return fmt.Errorf("journal task %d: %w", cell.Task, err)
			}
			attempt, executeErr := backend.Execute(ctx, cell, fixture)
			if executeErr != nil {
				_ = backend.Cleanup(ctx, cell, fixture)
				return fmt.Errorf("execute task %d: %w", cell.Task, executeErr)
			}
			result.Attempts = append(result.Attempts, attempt)
			if err := backend.FinishAttempt(ctx, cell, len(result.Attempts), attempt); err != nil {
				_ = backend.Cleanup(ctx, cell, fixture)
				return fmt.Errorf("finish journal task %d: %w", cell.Task, err)
			}
			if !RetryAllowed(len(result.Attempts), attempt.ProviderFailure, attempt.OutputStarted) {
				break
			}
		}
		result.Duration = time.Since(start)
		last := result.Attempts[len(result.Attempts)-1]
		if !(last.ProviderFailure && !last.OutputStarted) && last.Observation.Complete {
			expect, err := Expected(cell.Task, fixture)
			if err != nil {
				_ = backend.Cleanup(ctx, cell, fixture)
				return err
			}
			result.Score = ScoreTask(expect, last.Observation)
			result.Scored = true
			result.Measured = last.UsageAvailable
		}
		if err := emit(result); err != nil {
			_ = backend.Cleanup(ctx, cell, fixture)
			return fmt.Errorf("record task %d: %w", cell.Task, err)
		}
		if err := backend.Cleanup(ctx, cell, fixture); err != nil {
			return fmt.Errorf("cleanup task %d: %w", cell.Task, err)
		}
	}
	return nil
}
