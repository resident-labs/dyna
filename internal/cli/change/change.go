// Package change exposes session-linked changes and findings.
package change

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/notify"
	prstate "dyna-agent/internal/pr/state"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

// Options supplies service and session dependencies.
type Options struct {
	Service func() *watch.Service
	Session func(string) (subagent.Info, error)
}

func NewCommand() *cobra.Command { return Command(Options{}) }

func Command(o Options) *cobra.Command {
	if o.Service == nil {
		o.Service = watch.New
	}
	if o.Session == nil {
		o.Session = notify.Default().Sessions.Store.ResolveSnapshot
	}

	roomFor := func(s *watch.Service, ref string) (string, error) {
		r, err := s.Notify.Rooms.ResolveContext(ref, false)
		return r.ID, err
	}
	output := func(c *cobra.Command, value any) error {
		enc := json.NewEncoder(c.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	record := func(c *cobra.Command, s *watch.Service, key string) error {
		r, err := s.Store.Get(key)
		if err != nil {
			return err
		}
		return output(c, r)
	}
	root := &cobra.Command{Use: "change", Short: "Review a committed change: base, head and findings", Long: "Track committed changes from a worktree and record reviews and finding replies. A change is a branch with a base and an exact head; it needs no pull request. Results use JSON. Tracked pull requests merge automatically after exact-head approval and green CI.", RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}

	open := &cobra.Command{Use: "open", Short: "Open a change from the current worktree or link a PR with author notes", Long: "The author is the stamped Dyna session. With no Dyna stamp, root (the room owner) is the author.", Args: cobra.NoArgs}
	openRoom := open.Flags().String("room", "", "room name or ID; a stamped session may name only its own room")
	pr := open.Flags().String("pr", "", "link a GitHub PR number or URL instead of a local change")
	var opts watch.OpenChangeOptions
	open.Flags().StringVar(&opts.Base, "base", "", "base branch or commit (default: origin/main, then main)")
	open.Flags().StringVar(&opts.Title, "title", "", "change title (default: branch name)")
	open.Flags().StringVar(&opts.Notes.Intent, "intent", "", "what this change does and why")
	open.Flags().StringVar(&opts.Notes.Testing, "testing", "", "checks run and their results")
	open.Flags().StringVar(&opts.Notes.Risks, "risks", "", "known risks")
	open.Flags().StringVar(&opts.Notes.Questions, "questions", "", "questions for the reviewer")
	open.Flags().StringVar(&opts.Category, "category", "", "change category: fix, small, refactor, or move")
	skipReview := open.Flags().String("skip-review", "", "skip review with this recorded reason")
	open.Flags().StringVar(&opts.Goal, "goal", "", "link the change to this open room goal, such as g12 (default: your goal when unambiguous)")
	fixesText := open.Flags().String("fixes", "", "issues the PR closes on merge, such as D-5,#207 (needs --pr)")
	open.Flags().StringArrayVar(&opts.Dependencies, "depends-on", nil, "required change ID; repeat for each dependency")
	open.RunE = func(c *cobra.Command, _ []string) error {
		if c.Flags().Changed("skip-review") && strings.TrimSpace(*skipReview) == "" {
			return fmt.Errorf("--skip-review requires a reason")
		}
		if c.Flags().Changed("fixes") {
			fixes, err := prstate.ParseIssueRefs(*fixesText)
			if err != nil {
				return err
			}
			if len(fixes) == 0 || *pr == "" {
				return fmt.Errorf("--fixes links issues only on a GitHub PR, so pass --pr and at least one reference such as D-5 or #207")
			}
			opts.Fixes = fixes
		}
		if *skipReview != "" {
			opts.PolicyOverride.Review = "off"
			opts.PolicyOverride.Reason = *skipReview
		}
		s := o.Service()
		info, err := subagent.Actor(s.Notify.Rooms, o.Session, *openRoom)
		if err != nil {
			return err
		}
		switch opts.Category {
		case "", "fix", "small", "refactor", "move":
		default:
			return fmt.Errorf("category must be fix, small, refactor, or move")
		}
		notes, err := json.Marshal(opts.Notes)
		if err != nil {
			return err
		}
		if len(notes) > watch.MaxReviewBytes {
			return fmt.Errorf("author notes exceed %d bytes", watch.MaxReviewBytes)
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		// The engine opens the change as this caller (requirements item 3) and
		// accepts only a checkout of the room's repository; a PR link from any
		// other folder uses the room's workspace.
		if *pr != "" {
			if room, err := s.Notify.Rooms.Resolve(info.RoomID); err != nil || prview.CheckoutOfRoom(c.Context(), room, dir) != nil {
				dir = ""
			}
		}
		var opened prview.OpenedChange
		if err := engineclient.New("").Call(c.Context(), "OpenChange", []any{info.RoomID, info.ID, *pr, dir, opts}, &opened); err != nil {
			return err
		}
		return output(c, opened)
	}
	list := &cobra.Command{Use: "list", Short: "List changes in a room", Args: cobra.NoArgs}
	listRoom := list.Flags().String("room", "", "room name or ID (default: current stamped room)")
	list.RunE = func(c *cobra.Command, _ []string) error {
		s := o.Service()
		id, err := roomFor(s, *listRoom)
		if err != nil {
			return err
		}
		rows, err := s.Changes(id)
		if err != nil {
			return err
		}
		return output(c, rows)
	}
	show := &cobra.Command{Use: "show <change-id>", Short: "Show a change and its review findings", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error { return record(c, o.Service(), a[0]) }}
	closeCmd := &cobra.Command{Use: "close <change-id>", Short: "Close a change and stop watching it", Args: cobra.ExactArgs(1)}
	closeCmd.RunE = func(c *cobra.Command, a []string) error {
		s := o.Service()
		r, err := s.Store.Get(a[0])
		if err != nil {
			return err
		}
		if err := engineclient.New("").Call(c.Context(), "CloseChange", []any{r.RoomID, r.Key}, nil); err != nil {
			return err
		}
		return record(c, s, a[0])
	}
	reply := &cobra.Command{Use: "reply <change-id> <finding-id> fixed|wont-fix|question", Short: "Record the author's reply to a finding", Args: cobra.ExactArgs(3)}
	message := reply.Flags().String("message", "", "reply text (required)")
	_ = reply.MarkFlagRequired("message")
	reply.RunE = func(c *cobra.Command, a []string) error {
		s := o.Service()
		r, err := s.Store.Get(a[0])
		if err != nil {
			return err
		}
		if err := engineclient.New("").Call(c.Context(), "ReplyToFinding", []any{r.RoomID, r.Key, a[1], a[2], *message}, nil); err != nil {
			return err
		}
		return record(c, s, a[0])
	}
	verdict := &cobra.Command{Use: "verdict <change-id> approve|changes|approve-fast|stop-early [Markdown review]", Short: "Record a typed review result for the assigned change head", Long: "Record one typed review result: approve, changes, approve-fast, or stop-early. Supply the assigned --head. Use --body-file for multiline Markdown, or - for stdin. Local reviews need no GitHub review ID. GitHub assignments require --review-id for your published review. An approve-fast result lands fast only when Dyna's changed-path rules also rate the change low risk. A stop-early result needs --reason: superseded, duplicate, empty, or too-large. This command does not post to GitHub.", Args: cobra.RangeArgs(2, 3)}
	head := verdict.Flags().String("head", "", "assigned change head SHA (required)")
	_ = verdict.MarkFlagRequired("head")
	bodyFile := verdict.Flags().String("body-file", "", fmt.Sprintf("Markdown file (- for stdin), up to %d bytes", watch.MaxReviewBytes))
	findingsFile := verdict.Flags().String("findings-file", "", fmt.Sprintf("file with a JSON array of findings, up to %d bytes", watch.MaxFindingsBytes))
	reviewID := verdict.Flags().Int64("review-id", 0, "GitHub ID of your published review, when required")
	reason := verdict.Flags().String("reason", "", "stop reason for stop-early: superseded, duplicate, empty, or too-large")
	verdict.RunE = func(c *cobra.Command, a []string) error {
		if strings.TrimSpace(*head) == "" {
			return fmt.Errorf("--head is required")
		}
		summary := ""
		if *bodyFile != "" {
			if len(a) == 3 {
				return fmt.Errorf("choose a positional review or --body-file")
			}
			body, err := readBounded(c, *bodyFile, watch.MaxReviewBytes)
			if err != nil {
				return err
			}
			summary = string(body)
		} else if len(a) == 3 {
			summary = a[2]
		}
		if len(summary) > watch.MaxReviewBytes {
			return fmt.Errorf("review exceeds %d bytes; nothing was saved", watch.MaxReviewBytes)
		}
		var findings []watch.Finding
		if *findingsFile != "" {
			if *bodyFile == "-" && *findingsFile == "-" {
				return fmt.Errorf("only one input can read stdin")
			}
			data, err := readBounded(c, *findingsFile, watch.MaxFindingsBytes)
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
				return fmt.Errorf("findings must be a JSON array")
			}
			if err := json.Unmarshal(data, &findings); err != nil {
				return fmt.Errorf("decode findings: %w", err)
			}
		}
		s := o.Service()
		r, err := s.Store.Get(a[0])
		if err != nil {
			return err
		}
		result := watch.ReviewResult{Verdict: prstate.ReviewVerdict(a[1]), Findings: findings, Summary: summary, Head: *head, ReviewID: *reviewID, StopReason: prstate.StopReason(*reason)}
		var saved prview.VerdictResult
		if err := engineclient.New("").Call(c.Context(), "RecordReviewResult", []any{r.RoomID, r.Key, result}, &saved); err != nil {
			return err
		}
		if saved.Notice != "" {
			fmt.Fprintln(c.OutOrStdout(), saved.Notice)
			return nil
		}
		return record(c, s, r.Key)
	}
	root.AddCommand(open, list, show, closeCmd, reply, verdict, policyCommand(o, roomFor))
	return root
}

// Read one extra byte so oversized input is rejected instead of silently clipped.
func readBounded(c *cobra.Command, path string, max int) ([]byte, error) {
	reader := c.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader = f
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, fmt.Errorf("%s exceeds %d bytes; nothing was saved", path, max)
	}
	return data, nil
}
