package harness

import (
	"bytes"
	"context"
	"os"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// runInProcess replaces only the process boundary for recorded attempts. It
// feeds real protocol records through normalization and the ordinary attempt
// result, so runJournaledState still owns error classification and recovery.
func runInProcess(ctx context.Context, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, outputPath, normalizedOutputPath string, output *workeroutput.Declaration, state *runState, run ProcessRunner) attempt {
	var raw, normalized *os.File
	if outputPath != "" {
		raw, _ = os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0)
	}
	if raw != nil {
		defer raw.Close()
	}
	if normalizedOutputPath != "" {
		normalized, _ = os.OpenFile(normalizedOutputPath, os.O_APPEND|os.O_WRONLY, 0)
	}
	if normalized != nil {
		defer normalized.Close()
	}
	normalizer := workeroutput.NewNormalizer(output, normalized)
	var protocolErr error
	observe := func(line string) {
		if spec.streamInput && protocolErr == nil {
			protocolErr = workeroutput.ClaudeResultError(line)
		}
		record := workeroutput.DecodeLine(output, line)
		tracker.observeRecord(record, line)
		state.observeRecord(record)
		_ = normalizer.WriteRecord(record)
	}
	stdout := newObservedBuffer(observe, raw)
	var stderr bytes.Buffer
	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, spec.prompt); err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	if steering != nil && steering.opts.StartGate != nil {
		if err := steering.opts.StartGate(ctx, spec.prompt, func() error { return nil }); err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	state.beginProcess()
	if steering != nil && steering.opts.OnStart != nil {
		steering.opts.OnStart()
	}
	runErr := run(ctx, append([]string(nil), spec.argv...), stdout, &stderr)
	stdout.FlushPending(observe)
	if spec.streamInput && runErr == nil {
		// The real Claude process supervisor takes this error from its terminal
		// stream result, rather than from the process exit status.
		runErr = protocolErr
	}
	if journal != nil {
		journal.poll()
	}
	var messages []runstore.SteeringMessage
	if steering != nil {
		_ = steering.activateIfReady(tracker.idValue())
		steering.poll()
		messages = steering.take()
	}
	return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, ctx.Err() != nil, false, false, messages, steering.errValue())
}
