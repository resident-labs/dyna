package computer

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

//go:embed browser_trace.mjs
var traceScript []byte

//go:embed browser_page.mjs
var pageScript []byte

// traceCPUNote tells the reader the numbers include the trace's own cost.
const traceCPUNote = "The trace adds CPU load: a page observer, a console and request listener, and two forced garbage collections. Long task counts can read slightly high."

// TraceReport holds the r13 numbers from one trace.
type TraceReport struct {
	URL                string   `json:"url"`
	TimeToUsableMS     *int     `json:"time_to_usable_ms"`
	TimeToUsableSource string   `json:"time_to_usable_source"`
	LongTasks          int      `json:"long_tasks_over_200ms"`
	HeapGrowthBytes    int64    `json:"heap_growth_bytes"`
	RequestCount       int      `json:"request_count"`
	ConsoleErrors      []string `json:"console_errors"`
	Screenshot         string   `json:"screenshot"`
	Note               string   `json:"note"`
}

// NewBrowserTraceCommand records the r13 numbers for the page in the shared browser.
func NewBrowserTraceCommand() *cobra.Command {
	root := &cobra.Command{Use: "trace", Short: "Measure page load and use: time to usable, long tasks, heap growth, requests, console errors"}
	root.AddCommand(&cobra.Command{Use: "start [URL]", Short: "Reload the page (or open URL) and start measuring", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		id, roomID, err := resolveActor()
		if err != nil {
			return err
		}
		if _, err := computers.Ensure(c.Context(), id, roomID); err != nil {
			return err
		}
		if _, err := startHolder(c.Context(), id, "trace", traceScript, false, args...); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "trace started; use the page, then run 'dyna browser trace stop'")
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "stop", Short: "Stop measuring and print the numbers", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		id, _, err := resolveActor()
		if err != nil {
			return err
		}
		dir := holderDir(id, "trace")
		pid := holderPID(dir)
		if pid == 0 {
			return errors.New("no trace is running; run 'dyna browser trace start' first")
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("trace collector is gone: %w", err)
		}
		result := filepath.Join(dir, "result.json")
		if err := waitForFile(c.Context(), result, 90*time.Second); err != nil {
			return err
		}
		b, err := os.ReadFile(result)
		if err != nil {
			return err
		}
		var report TraceReport
		if err := json.Unmarshal(b, &report); err != nil {
			return err
		}
		report.Note = traceCPUNote
		return printJSON(c, report)
	}})
	return root
}
