// Package profilecheck runs the explicit, bounded profile health probe.
package profilecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/harness"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// A direct harness call also checks profiles that workflows cannot select.
func Run(ctx context.Context, p profile.Profile) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "dyna-profile-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	outputPath := filepath.Join(dir, "output.jsonl")
	if err := os.WriteFile(outputPath, nil, 0600); err != nil {
		return err
	}
	definition, _ := profile.HarnessDefinitionFor(p.Harness)
	result, err := harness.RunWithOptions(ctx, p, definition.ProbePrompt, dir, false,
		harness.JournalOptions{NormalizedOutputPath: outputPath, Output: p.EffectiveOutput()},
		harness.SteeringOptions{}, harness.SessionOptions{OnSession: func(string) {}},
		harness.ExecutionOptions{HealthProbe: true, KillOnCancel: true, DisableNativeSubagents: true})
	if err != nil {
		harnesshealth.New("").RecordProbe(p, result.CLIVersion, result.Observed, false)
		return err
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return err
	}
	err = Validate(p.EffectiveDialect(), result, data)
	harnesshealth.New("").RecordProbe(p, result.CLIVersion, result.Observed, err == nil)
	return err
}

func Validate(dialect string, result harness.Result, data []byte) error {
	caps := workeroutput.Capabilities(dialect)
	var transcript strings.Builder
	tool := false
	for _, line := range strings.Split(string(data), "\n") {
		var event workeroutput.Event
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Kind == "message" && event.Role == "assistant" {
			transcript.WriteString(event.Text)
		}
		if event.Kind == "tool" {
			tool = true
		}
	}
	if !strings.Contains(transcript.String(), profile.HarnessProbeReply) {
		return fmt.Errorf("expected reply missing from text events")
	}
	if caps.ToolEvents && !tool {
		return fmt.Errorf("tool events missing")
	}
	if caps.Usage == "yes" && result.Usage.Total() <= 0 {
		return fmt.Errorf("positive token usage missing")
	}
	return nil
}
