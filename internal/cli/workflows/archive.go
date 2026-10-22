package workflows

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runsvc"
)

// listedRun is a run catalog row. Archived is omitted for live runs, so the
// default `runs list --json` shape is unchanged.
type listedRun struct {
	runstore.Meta
	Archived bool `json:"archived,omitempty"`
}

func listRuns(archivedOnly, all bool) ([]listedRun, error) {
	var metas []runstore.Meta
	var err error
	switch {
	case archivedOnly:
		metas, err = runstore.ListArchived()
	case all:
		metas, err = runstore.ListAll()
	default:
		metas, err = runstore.List()
	}
	if err != nil {
		return nil, err
	}
	out := make([]listedRun, len(metas))
	for i, m := range metas {
		out[i] = listedRun{Meta: m, Archived: archivedOnly || (all && runstore.IsArchived(m.ID))}
	}
	return out, nil
}

// parseAge accepts Go durations plus a whole-day suffix ("7d").
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid --older-than %q", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid --older-than %q (use e.g. 24h, 90m, 7d)", s)
	}
	return d, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func newArchiveCommand() *cobra.Command {
	var olderThan, statuses string
	var allFinished, dryRun bool
	cmd := &cobra.Command{
		Use:   "archive [run-id...]",
		Short: "Move finished runs out of the live catalog (still resolvable by id)",
		Long: "Move finished runs (ok, canceled, error) from the live runs directory into\n" +
			"the archive (" + "$" + runstore.ArchiveDirEnv + ", default $XDG_DATA_HOME/dyna/archive/runs).\n" +
			"Archived runs are hidden from `dyna runs list` and the desktop app, but every id-based\n" +
			"command keeps working: runs show/output/graph/wait, and `dyna run --resume <id>`.\n\n" +
			"Running and paused runs, runs still starting, and every resume ancestor of an\n" +
			"active run are never archived. With filters, resume ancestors of runs that\n" +
			"stay live are kept live too. Each move is one rename (or copy, verify, remove\n" +
			"across filesystems), so a run directory is never split or altered.",
		RunE: func(c *cobra.Command, ids []string) error {
			sel := runstore.ArchiveSelector{IDs: ids, AllFinished: allFinished, Now: time.Now()}
			if olderThan != "" {
				d, err := parseAge(olderThan)
				if err != nil {
					return err
				}
				if d == 0 {
					return fmt.Errorf("--older-than must be positive")
				}
				sel.OlderThan = d
			}
			if statuses != "" {
				sel.Statuses = strings.Split(statuses, ",")
			}
			selected, skips, err := runstore.PlanArchive(sel)
			if err != nil {
				return err
			}
			out, errOut := c.OutOrStdout(), c.ErrOrStderr()
			for _, s := range skips {
				fmt.Fprintf(errOut, "kept %s: %s\n", s.ID, s.Reason)
			}
			verb := "archived"
			if dryRun {
				verb = "would archive"
			}
			var total int64
			moved := 0
			var failures []string
			for _, m := range selected {
				size := runstore.DirSize(runstore.RunDir(m.ID))
				ended := m.EndedAt
				if ended.IsZero() {
					ended = m.StartedAt
				}
				line := fmt.Sprintf("%s %-32s %-9s %-20s %9s  %s", verb, m.ID, m.Status, ended.Local().Format("2006-01-02 15:04:05"), humanBytes(size), m.Name)
				if dryRun {
					fmt.Fprintln(out, line)
					total += size
					moved++
					continue
				}
				var res runsvc.Archived
				if err := engineclient.New("").Call(c.Context(), "ArchiveRun", []any{m.ID}, &res); err != nil {
					failures = append(failures, err.Error())
					fmt.Fprintln(errOut, "error: "+err.Error())
					continue
				}
				fmt.Fprintln(out, line)
				total += size
				moved++
			}
			summary := fmt.Sprintf("%s %d run(s), %s, to %s", verb, moved, humanBytes(total), runstore.ArchiveDir())
			if dryRun {
				summary += " (dry run; nothing moved)"
			}
			fmt.Fprintln(out, summary)
			if len(failures) > 0 {
				return fmt.Errorf("%d run(s) could not be archived", len(failures))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "only runs that finished at least this long ago (e.g. 24h, 7d)")
	cmd.Flags().StringVar(&statuses, "status", "", "only these finished statuses, comma-separated (ok,canceled,error)")
	cmd.Flags().BoolVar(&allFinished, "all-finished", false, "every finished run (still subject to --older-than/--status)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would move; change nothing")
	return cmd
}

func newUnarchiveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unarchive <run-id>...",
		Short: "Move archived runs back into the live catalog",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, ids []string) error {
			failed := 0
			for _, id := range ids {
				var dir string
				if err := engineclient.New("").Call(c.Context(), "UnarchiveRun", []any{id}, &dir); err != nil {
					fmt.Fprintln(c.ErrOrStderr(), "error: "+err.Error())
					failed++
					continue
				}
				fmt.Fprintf(c.OutOrStdout(), "unarchived %s -> %s\n", id, dir)
			}
			if failed > 0 {
				return fmt.Errorf("%d run(s) could not be unarchived", failed)
			}
			return nil
		},
	}
}
