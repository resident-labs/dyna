package workflows

import (
	"dyna-agent/internal/transcript"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

const outputFollowInterval = 400 * time.Millisecond

type outputOptions struct {
	raw     bool
	asJSON  bool
	follow  bool
	offset  int64
	bytes   int64
	timeout int
}

type workerOutputReport struct {
	RunID       string               `json:"runId"`
	AgentID     int                  `json:"agentId"`
	Mode        string               `json:"mode"`
	Events      []workeroutput.Event `json:"events,omitempty"`
	Text        string               `json:"text,omitempty"`
	Data        string               `json:"data,omitempty"`
	ValidUTF8   *bool                `json:"validUtf8,omitempty"`
	Offset      int64                `json:"offset"`
	NextOffset  int64                `json:"nextOffset"`
	FileSize    int64                `json:"fileSize"`
	PartialTail bool                 `json:"partialTail"`
	Truncated   bool                 `json:"truncated"`
	Missing     bool                 `json:"missing,omitempty"`
	Complete    bool                 `json:"complete"`
}

func newOutputCommand() *cobra.Command {
	var opts outputOptions
	cmd := &cobra.Command{
		Use:   "output <run-id> <agent-id>",
		Short: "Show a worker's normalized Output or exact Raw stdout",
		Long: `Show one worker's live or finished output artifacts.

Default prints the normalized Output view: schema-v1 events from output.jsonl.
Only complete JSONL records are shown. A partial live tail stays uncommitted
and is never treated as a finished record.

--raw prints stdout.log exactly, with no added newline. --json emits a
machine envelope with events or text, byte offsets, and complete/partialTail/
truncated flags. --json --follow writes one compact envelope per update as
JSONL. --offset and --bytes select an artifact byte range; --follow keeps
reading while the worker or run is still active.`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, a []string) error {
			agentID, err := strconv.Atoi(a[1])
			if err != nil {
				return fmt.Errorf("agent id must be a positive integer")
			}
			return runWorkerOutput(c, a[0], agentID, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.raw, "raw", false, "print exact harness stdout.log bytes instead of normalized output.jsonl")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "machine-readable envelope with offsets and completeness flags")
	cmd.Flags().BoolVarP(&opts.follow, "follow", "f", false, "keep reading while the worker or run is active")
	cmd.Flags().Int64Var(&opts.offset, "offset", 0, "start at this artifact byte offset")
	cmd.Flags().Int64Var(&opts.bytes, "bytes", 0, "max artifact bytes to read from --offset (0 = unbounded)")
	cmd.Flags().IntVar(&opts.timeout, "timeout", 0, "give up after N seconds when following (0 = until the worker finishes)")
	return cmd
}

func runWorkerOutput(c *cobra.Command, runID string, agentID int, opts outputOptions) error {
	if opts.offset < 0 {
		return fmt.Errorf("--offset must be >= 0")
	}
	if opts.bytes < 0 {
		return fmt.Errorf("--bytes must be >= 0")
	}
	if opts.raw {
		if _, err := runstore.AgentOutputPath(runID, agentID); err != nil {
			return err
		}
	} else if _, err := runstore.AgentNormalizedOutputPath(runID, agentID); err != nil {
		return err
	}
	if _, err := runstore.ReadMeta(runID); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("run %s not found", runID)
		}
		return err
	}

	var progress outputProgress
	offset := opts.offset
	remaining := opts.bytes
	var deadline time.Time
	if opts.timeout > 0 {
		deadline = time.Now().Add(time.Duration(opts.timeout) * time.Second)
	}
	w := c.OutOrStdout()
	sawArtifact := false
	emittedComplete := false
	emittedJSON := false
	human := humanOutputWriter{w: w}
	var stableTail outputTail

	for {
		if err := progress.refresh(runID, agentID); err != nil && !os.IsNotExist(err) {
			return err
		}
		limit := remaining
		report, err := emitWorkerOutput(w, runID, agentID, offset, limit, opts, progress, &human, &emittedJSON)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			report = missingOutputReport(runID, agentID, offset, opts.raw, progress)
		} else {
			sawArtifact = true
			emittedComplete = emittedComplete || report.Complete
		}

		if opts.bytes > 0 && !report.Missing {
			if report.Truncated {
				remaining = 0
			} else if advanced := report.NextOffset - offset; advanced > 0 {
				remaining -= advanced
			}
		}
		if !report.Missing {
			offset = report.NextOffset
		}

		if !opts.follow {
			if err := human.flush(); err != nil {
				return err
			}
			if report.Missing {
				return missingOutputError(runID, agentID, opts.raw)
			}
			return nil
		}
		if remaining == 0 && opts.bytes > 0 {
			return human.flush()
		}
		if followShouldStop(progress, report, &stableTail) {
			if err := human.flush(); err != nil {
				return err
			}
			if !sawArtifact {
				return missingOutputError(runID, agentID, opts.raw)
			}
			if opts.asJSON && !emittedJSON {
				if err := writeOutputJSON(w, report, true); err != nil {
					return err
				}
			} else if opts.asJSON && !emittedComplete && !report.PartialTail && !report.Missing {
				report.Complete = outputComplete(progress, false, report.Truncated, false)
				report.Events = nil
				report.Text = ""
				report.Data = ""
				report.ValidUTF8 = nil
				if err := writeOutputJSON(w, report, emittedJSON); err != nil {
					return err
				}
			}
			return nil
		}
		if opts.timeout > 0 && !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("timed out reading %s agent %d output (still active)", runID, agentID)
		}
		time.Sleep(outputFollowInterval)
	}
}

func emitWorkerOutput(w io.Writer, runID string, agentID int, offset, limit int64, opts outputOptions, progress outputProgress, human *humanOutputWriter, emittedJSON *bool) (workerOutputReport, error) {
	if opts.raw && !opts.asJSON {
		report, err := copyRawOutput(w, runID, agentID, offset, limit, progress)
		return report, err
	}
	report, err := readWorkerOutput(runID, agentID, offset, limit, opts.raw, progress)
	if err != nil {
		return report, err
	}
	newData := len(report.Events) > 0 || report.Text != "" || report.Data != ""
	if opts.asJSON {
		if newData || !opts.follow || report.Complete {
			if err := writeOutputJSON(w, report, opts.follow); err != nil {
				return report, err
			}
			*emittedJSON = true
		}
		return report, nil
	}
	return report, human.write(report.Events)
}

type outputTail struct {
	next int64
	size int64
	seen bool
}

func (t *outputTail) stable(next, size int64) bool {
	if t.seen && t.next == next && t.size == size {
		return true
	}
	t.next = next
	t.size = size
	t.seen = true
	return false
}

func followShouldStop(progress outputProgress, report workerOutputReport, tail *outputTail) bool {
	if !progress.finished() {
		return false
	}
	if report.Missing || !report.PartialTail {
		return true
	}
	// Worker/run is done but a partial JSONL tail remains. Stop once
	// nextOffset/fileSize stop changing so --timeout 0 cannot hang.
	return tail.stable(report.NextOffset, report.FileSize)
}

type outputProgress struct {
	eventOffset int64
	agentDone   bool
	runDone     bool
}

func (p *outputProgress) refresh(runID string, agentID int) error {
	meta, err := runstore.ReadMeta(runID)
	if err != nil {
		return err
	}
	p.runDone = meta.Status != "running"
	events, next, err := runstore.ReadEventsFrom(runID, p.eventOffset)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	p.eventOffset = next
	for _, event := range events {
		if event.T == "agent_end" && event.ID == agentID {
			p.agentDone = true
		}
	}
	return nil
}

func (p outputProgress) finished() bool { return p.runDone || p.agentDone }

func readWorkerOutput(runID string, agentID int, offset, limit int64, raw bool, progress outputProgress) (workerOutputReport, error) {
	if raw {
		return readRawOutput(runID, agentID, offset, limit, progress)
	}
	return readNormalizedOutput(runID, agentID, offset, limit, progress)
}

func readNormalizedOutput(runID string, agentID int, offset, limit int64, progress outputProgress) (workerOutputReport, error) {
	path, err := runstore.AgentNormalizedOutputPath(runID, agentID)
	if err != nil {
		return workerOutputReport{}, err
	}
	size, pastEOF, err := outputWindow(path, offset)
	if err != nil {
		return workerOutputReport{}, err
	}
	if pastEOF {
		return emptyOutputWindow(runID, agentID, "output", offset, size, progress), nil
	}
	events, next, err := runstore.ReadAgentNormalizedOutputLimitedFrom(runID, agentID, offset, limit)
	if err != nil {
		return workerOutputReport{}, err
	}
	truncated := limit > 0 && size > offset+limit
	partial := size > next && !truncated
	return workerOutputReport{
		RunID:       runID,
		AgentID:     agentID,
		Mode:        "output",
		Events:      events,
		Offset:      offset,
		NextOffset:  next,
		FileSize:    size,
		PartialTail: partial,
		Truncated:   truncated,
		Complete:    outputComplete(progress, false, truncated, partial),
	}, nil
}

func readRawOutput(runID string, agentID int, offset, limit int64, progress outputProgress) (workerOutputReport, error) {
	path, err := runstore.AgentOutputPath(runID, agentID)
	if err != nil {
		return workerOutputReport{}, err
	}
	size, pastEOF, err := outputWindow(path, offset)
	if err != nil {
		return workerOutputReport{}, err
	}
	if pastEOF {
		report := emptyOutputWindow(runID, agentID, "raw", offset, size, progress)
		valid := true
		report.Text = ""
		report.ValidUTF8 = &valid
		return report, nil
	}
	data, next, err := runstore.ReadAgentOutputLimitedFrom(runID, agentID, offset, limit)
	if err != nil {
		return workerOutputReport{}, err
	}
	truncated := limit > 0 && size > offset+limit
	partial := size > next && !truncated
	report := workerOutputReport{
		RunID:       runID,
		AgentID:     agentID,
		Mode:        "raw",
		Offset:      offset,
		NextOffset:  next,
		FileSize:    size,
		PartialTail: partial,
		Truncated:   truncated,
		Complete:    outputComplete(progress, false, truncated, partial),
	}
	if utf8.Valid(data) {
		valid := true
		report.Text = string(data)
		report.ValidUTF8 = &valid
	} else {
		valid := false
		report.Data = base64.StdEncoding.EncodeToString(data)
		report.ValidUTF8 = &valid
	}
	return report, nil
}

func missingOutputReport(runID string, agentID int, offset int64, raw bool, progress outputProgress) workerOutputReport {
	mode := "output"
	if raw {
		mode = "raw"
	}
	return workerOutputReport{
		RunID: runID, AgentID: agentID, Mode: mode,
		Offset: offset, NextOffset: offset, Missing: true,
		Complete: outputComplete(progress, true, false, false),
	}
}

func outputComplete(progress outputProgress, missing, truncated, partialTail bool) bool {
	return progress.finished() && !missing && !truncated && !partialTail
}

func writeOutputJSON(w io.Writer, report workerOutputReport, compact bool) error {
	payload := outputReportJSON(report)
	var b []byte
	var err error
	if compact {
		b, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		b = append(b, '\n')
	} else {
		b, err = json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		b = append(b, '\n')
	}
	_, err = w.Write(b)
	return err
}

func outputReportJSON(report workerOutputReport) map[string]any {
	out := map[string]any{
		"runId":       report.RunID,
		"agentId":     report.AgentID,
		"mode":        report.Mode,
		"offset":      report.Offset,
		"nextOffset":  report.NextOffset,
		"fileSize":    report.FileSize,
		"partialTail": report.PartialTail,
		"truncated":   report.Truncated,
		"complete":    report.Complete,
	}
	if report.Missing {
		out["missing"] = true
	}
	if report.Mode == "output" {
		events := report.Events
		if events == nil {
			events = []workeroutput.Event{}
		}
		out["events"] = events
	} else {
		if report.Data != "" {
			out["data"] = report.Data
		} else {
			out["text"] = report.Text
		}
		if report.ValidUTF8 != nil {
			out["validUtf8"] = *report.ValidUTF8
		}
	}
	return out
}

func copyRawOutput(w io.Writer, runID string, agentID int, offset, limit int64, progress outputProgress) (workerOutputReport, error) {
	path, err := runstore.AgentOutputPath(runID, agentID)
	if err != nil {
		return workerOutputReport{}, err
	}
	size, pastEOF, err := outputWindow(path, offset)
	if err != nil {
		return workerOutputReport{}, err
	}
	if pastEOF {
		return emptyOutputWindow(runID, agentID, "raw", offset, size, progress), nil
	}
	_, next, err := runstore.CopyAgentOutputTo(w, runID, agentID, offset, limit)
	if err != nil {
		return workerOutputReport{}, err
	}
	truncated := limit > 0 && size > offset+limit
	partial := size > next && !truncated
	return workerOutputReport{
		RunID:       runID,
		AgentID:     agentID,
		Mode:        "raw",
		Offset:      offset,
		NextOffset:  next,
		FileSize:    size,
		PartialTail: partial,
		Truncated:   truncated,
		Complete:    outputComplete(progress, false, truncated, partial),
	}, nil
}

func outputWindow(path string, offset int64) (int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	size := info.Size()
	return size, offset > size, nil
}

func emptyOutputWindow(runID string, agentID int, mode string, offset, size int64, progress outputProgress) workerOutputReport {
	return workerOutputReport{
		RunID:      runID,
		AgentID:    agentID,
		Mode:       mode,
		Offset:     offset,
		NextOffset: size,
		FileSize:   size,
		Complete:   outputComplete(progress, false, false, false),
	}
}

type humanOutputWriter struct {
	w             io.Writer
	emitted       bool
	openReasoning *workeroutput.Event
}

func (h *humanOutputWriter) write(events []workeroutput.Event) error {
	if h == nil || h.w == nil || len(events) == 0 {
		return nil
	}
	src := events
	if h.openReasoning != nil {
		src = make([]workeroutput.Event, 0, 1+len(events))
		src = append(src, *h.openReasoning)
		src = append(src, events...)
	}
	coalesced := workeroutput.CoalesceNormalizedEvents(src)
	i := 0
	if h.openReasoning != nil && len(coalesced) > 0 && coalesced[0].Kind == "reasoning" && coalesced[0].V == h.openReasoning.V {
		cur := coalesced[0]
		if extra := reasoningTextDelta(h.openReasoning.Text, cur.Text); extra != "" {
			if _, err := io.WriteString(h.w, extra); err != nil {
				return err
			}
		}
		if cur.State != "delta" || len(coalesced) > 1 {
			if _, err := fmt.Fprintln(h.w); err != nil {
				return err
			}
			h.openReasoning = nil
		} else {
			copied := cur
			h.openReasoning = &copied
		}
		i = 1
	} else if h.openReasoning != nil {
		if _, err := fmt.Fprintln(h.w); err != nil {
			return err
		}
		h.openReasoning = nil
	}
	for ; i < len(coalesced); i++ {
		event := coalesced[i]
		open := isOpenReasoningDelta(event) && i == len(coalesced)-1
		if err := h.writeBlock(event, open); err != nil {
			return err
		}
	}
	return nil
}

func (h *humanOutputWriter) writeBlock(event workeroutput.Event, open bool) error {
	text := formatNormalizedEvent(event)
	if text == "" {
		return nil
	}
	if h.emitted {
		if _, err := fmt.Fprintln(h.w); err != nil {
			return err
		}
	}
	if open {
		if _, err := fmt.Fprint(h.w, text); err != nil {
			return err
		}
		copied := event
		h.openReasoning = &copied
	} else {
		if _, err := fmt.Fprintln(h.w, text); err != nil {
			return err
		}
		h.openReasoning = nil
	}
	h.emitted = true
	return nil
}

func (h *humanOutputWriter) flush() error {
	if h == nil || h.openReasoning == nil {
		return nil
	}
	_, err := fmt.Fprintln(h.w)
	h.openReasoning = nil
	return err
}

func isOpenReasoningDelta(event workeroutput.Event) bool {
	return event.V == workeroutput.SchemaVersion && event.Kind == "reasoning" && event.State == "delta"
}

func reasoningTextDelta(prev, cur string) string {
	if strings.HasPrefix(cur, prev) {
		return cur[len(prev):]
	}
	return cur
}

func formatNormalizedEvent(event workeroutput.Event) string {
	if event.V != workeroutput.SchemaVersion {
		preview := normalizedPreview(event)
		if preview == "" {
			return fmt.Sprintf("EVENT V%d  %s", event.V, event.Kind)
		}
		return fmt.Sprintf("EVENT V%d  %s\n%s", event.V, event.Kind, preview)
	}
	switch event.Kind {
	case "message":
		role := strings.ToUpper(strings.TrimSpace(event.Role))
		if role == "" {
			role = "MESSAGE"
		}
		if event.Text == "" {
			return role
		}
		return role + "\n" + event.Text
	case "reasoning":
		if event.Text == "" {
			return "THINKING"
		}
		return "THINKING\n" + event.Text
	case "tool":
		// One condensed line per call; the Raw view (--raw) keeps the record.
		t := transcript.Summarize(event)
		if (event.State == "completed" || event.State == "failed") && event.ID != "" {
			if !t.Failed {
				return ""
			}
			return "  ↳ " + strings.TrimSuffix(strings.TrimPrefix(transcript.ToolAside(t), "("), ")")
		}
		line := "▸ " + transcript.Headline(t)
		if t.Count <= 1 && t.Target != "" {
			line += " " + strings.Join(strings.Fields(t.Target), " ")
		}
		if t.Output != "" && event.State != "started" {
			out := strings.Split(strings.TrimRight(t.Output, "\n"), "\n")
			if len(out) > 6 {
				out = append([]string{"…"}, out[len(out)-6:]...)
			}
			line += "\n  " + strings.Join(out, "\n  ")
		}
		return line
	case "status":
		status := strings.TrimSpace(event.Name + " " + event.State)
		if status == "" {
			status = event.Kind
		}
		if event.Text == "" {
			return "STATUS  " + status
		}
		return "STATUS  " + status + "\n" + event.Text
	case "raw":
		if event.Raw == "" {
			return "RAW"
		}
		return "RAW\n" + event.Raw
	default:
		preview := normalizedPreview(event)
		if preview == "" {
			return "EVENT  " + event.Kind
		}
		return "EVENT  " + event.Kind + "\n" + preview
	}
}

func normalizedPreview(event workeroutput.Event) string {
	for _, text := range []string{event.Raw, event.Text, event.Input, event.Output} {
		if text != "" {
			return text
		}
	}
	return ""
}

func missingOutputError(runID string, agentID int, raw bool) error {
	if raw {
		return fmt.Errorf("no raw output for %s agent %d: stdout.log not found", runID, agentID)
	}
	return fmt.Errorf("no normalized output for %s agent %d: output.jsonl not found", runID, agentID)
}
