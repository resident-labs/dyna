// Package pr exposes session-linked GitHub PR tracking and review verdicts.
package pr

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"dyna-agent/internal/authority"
	cliexport "dyna-agent/internal/cli/export"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	prstate "dyna-agent/internal/pr/state"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/subagent"

	"github.com/spf13/cobra"
)

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

	root := &cobra.Command{Use: "pr", Short: "Track the GitHub pull request the engine merges", Long: `Link a GitHub PR to its author session after gh pr create:
  dyna git pr track <number|url>

The engine checks CI, review comments, pushes and merge/close about once
a minute. It needs gh signed in on this host. Explicit tracking avoids guessing
the author when sessions share a branch. Track is idempotent: an existing link
keeps its author and state. A matching checkout updates its saved path.
Use --repo <path> to supply a checkout from another directory.
Use resume to restart paused CI wakes.
Pull request runs warn after 2 minutes and fail after 3 minutes, measured from
runner step time along the longest dependency chain. Set room budgets with dyna git pr budget --warn 2m --fail 3m.

Dyna manages a reviewer pool per room when CI passes. Each open, watched
change keeps one reviewer session, including after a verdict. Queued changes
wait when the pool is full. Sessions use reviewer-<PR> or reviewer-<short change id>.
Use dyna git pr reviewer --profile P to pin a profile,
--auto for automatic choice, or --restart to recreate the reviewer pool.
Use dyna git pr review on|off --room R to control automatic review. CodeRabbit review signals remain visible,
but cannot establish passing CI without another check, workflow or commit status.
Unknown external CI stays eligible. Agent reviews stay in Dyna.
Record full Markdown with dyna git pr verdict --head. Agents, including root
and workflow workers, cannot publish GitHub comments or reviews.
Only the owner can use dyna git pr comment or publish a GitHub review.
Comments from the owner, repository owners, members, collaborators and configured
trusted bots pass the trust gate. Other comments stay held for the owner in
home alerts and the PR room. Use held to list them, and admit or dismiss to decide.
Non-owner comments reach agents only as quoted data, not instructions.
Every kept comment goes to the author in one batch. Mark obvious bot spam with
dyna git pr noise <pr> <comment-id>; noise list and noise rm inspect or remove
learned repo-scoped templates. Every wake reports the drop count and rule IDs.

Three consecutive failed CI attempts pause CI wakes. Escalations walk from the
author up its parent chain to the first open session. If none is open, a CI
or land notice starts one agent for that head and makes it the author. Other
notices, or a failed start, go to the owner. The active turn stays running.
New owner comments still wake it. Passing CI or resume clears that pause. Dyna merges tracked pull requests automatically when the current head has
a Dyna approval and passing CI. Read dyna git pr merge-status for the queue.`, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}

	resolve := func(c *cobra.Command, text string) (*watch.Service, watch.Ref, error) {
		s := o.Service()
		dir, err := os.Getwd()
		if err != nil {
			return s, watch.Ref{}, err
		}
		r, err := s.Resolve(c.Context(), text, dir)
		return s, r, err
	}
	output := func(c *cobra.Command, value any) error {
		enc := json.NewEncoder(c.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	track := &cobra.Command{Use: "track <number|url>", Short: "Link a PR to this session, or to root with no Dyna stamp", Args: cobra.ExactArgs(1)}
	trackRepo := track.Flags().String("repo", "", "matching repository checkout path")
	trackGoal := track.Flags().String("goal", "", "link the PR to this open room goal, such as g12 (default: your goal when unambiguous; never replaces a link unless given)")
	trackFixes := track.Flags().String("fixes", "", "issues the PR closes on merge, such as D-5,#207; replaces the list when given")
	track.RunE = func(c *cobra.Command, a []string) error {
		s := o.Service()
		info, err := subagent.Actor(s.Notify.Rooms, o.Session, "")
		if err != nil {
			return err
		}
		var fixes []prstate.IssueRef
		if c.Flags().Changed("fixes") {
			fixes, err = prstate.ParseIssueRefs(*trackFixes)
			if err != nil {
				return err
			}
			if len(fixes) == 0 {
				return fmt.Errorf("--fixes needs at least one issue such as D-5 or #207")
			}
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		if *trackRepo != "" {
			dir = *trackRepo
		} else if rec, err := s.Notify.Rooms.Resolve(info.RoomID); err != nil || prview.CheckoutOfRoom(c.Context(), rec, dir) != nil {
			// The engine accepts only a checkout of the room's repository; from
			// any other folder, it tracks from the room's workspace.
			dir = ""
		}
		// The engine tracks as this caller (requirements item 3) and checks
		// that dir is a checkout of the room's and the PR's repository.
		var tracked prview.TrackedPR
		if err := engineclient.New("").Call(c.Context(), "TrackPRRef", []any{info.RoomID, info.ID, a[0], dir, *trackGoal, fixes}, &tracked); err != nil {
			return err
		}
		return output(c, tracked)
	}
	mergeStatus := &cobra.Command{Use: "merge-status", Short: "Show automatic merge eligibility and order", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var rows []watch.MergeQueueItem
		if err := engineclient.New("").Call(c.Context(), "MergeStatus", nil, &rows); err != nil {
			return err
		}
		return output(c, rows)
	}}
	root.AddCommand(mergeStatus)
	mergeFix := &cobra.Command{Use: "merge-fix <number|url>", Short: "Owner: merge one approved green PR while main CI is red", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, ref, err := resolve(c, args[0])
		if err != nil {
			return err
		}
		return prWrite(c, s, ref.Key(), "MergeFixPR")
	}}
	root.AddCommand(mergeFix)
	var history bool
	show := &cobra.Command{Use: "show <number|url>", Short: "Show PR status, pending notices and delivered notice count", Long: "Show current tracking, CI, reviewer, polling schedule and pause state as JSON. Pending notifications include full bodies. Delivered notifications appear only in deliveredNotifications, a count. Add --history to include delivered notification bodies too. --json uses the same output shape.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		rec, err := s.Show(r.Key())
		if err != nil {
			return err
		}
		delivered := 0
		notices := make([]watch.Notice, 0, len(rec.Notices))
		for _, notice := range rec.Notices {
			if notice.Done {
				delivered++
			}
			if history || !notice.Done && notice.SupersededBy == "" {
				notices = append(notices, notice)
			}
		}
		rec.Notices = notices
		return output(c, struct {
			watch.PRDetails
			DeliveredNotifications int `json:"deliveredNotifications"`
		}{rec, delivered})
	}}
	show.Flags().BoolVar(&history, "history", false, "include delivered notification bodies")
	show.Flags().Bool("json", false, "output JSON (the default)")
	list := &cobra.Command{Use: "list", Short: "List tracked PRs and their pause reasons", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		st, err := o.Service().Store.Read()
		if err != nil {
			return err
		}
		return output(c, st.Records)
	}}
	untrack := &cobra.Command{Use: "untrack <number|url>", Short: "Stop watching a PR while keeping its history", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		return prWrite(c, s, r.Key(), "UntrackPR")
	}}

	transfer := &cobra.Command{Use: "transfer <number|url>", Short: "Move a tracked PR author to another session", Args: cobra.ExactArgs(1)}
	target := transfer.Flags().String("to", "", "open session in the PR's room (required)")
	transferJSON := transfer.Flags().Bool("json", false, "output the full PR record as JSON")
	role := transfer.Flags().String("role", "author", "role to transfer: author (reviewers are managed by the room)")
	_ = transfer.MarkFlagRequired("to")
	transfer.RunE = func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		to, err := o.Session(strings.TrimPrefix(*target, "@"))
		if err != nil {
			return err
		}
		cur, err := s.Store.Get(r.Key())
		if err != nil {
			return err
		}
		// The engine records the caller's checkout only from the room's repository.
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		if room, err := s.Notify.Rooms.Resolve(cur.RoomID); err != nil || prview.CheckoutOfRoom(c.Context(), room, dir) != nil {
			dir = ""
		}
		var rec watch.Record
		if err := engineclient.New("").Call(c.Context(), "TransferPR", []any{cur.RoomID, r.Key(), *role, to.ID, dir}, &rec); err != nil {
			return err
		}
		if *transferJSON {
			return output(c, rec)
		}
		_, err = fmt.Fprintf(c.OutOrStdout(), "PR #%d author transferred to %s (%s).\n", rec.Number, to.Address(), rec.Author)
		return err
	}
	resume := &cobra.Command{Use: "resume <number|url>", Short: "Resume tracking and clear the CI wake pause", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		return prWrite(c, s, r.Key(), "ResumePR")
	}}

	hold := &cobra.Command{Use: "hold <number|url> <reason>", Short: "Hold a PR as a GitHub draft and post the reason", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		return prWrite(c, s, r.Key(), "SetPRDraft", true, a[1])
	}}
	release := &cobra.Command{Use: "release <number|url>", Short: "Release a PR from its GitHub draft hold", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		return prWrite(c, s, r.Key(), "SetPRDraft", false, "")
	}}

	require := &cobra.Command{Use: "require <number|url> <reviewer>...", Short: "Require reviewers on a tracked PR", Long: "Add reviewers that must review a tracked PR. The engine resolves and checks each reviewer. Adding a reviewer that is already required keeps the PR unchanged. Reviewers are open sessions in the PR's room, not profiles. Draft PRs get no automatic review.", Args: func(_ *cobra.Command, a []string) error {
		if len(a) < 2 {
			return fmt.Errorf("name at least one reviewer after the PR")
		}
		return nil
	}, RunE: func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		return prWrite(c, s, r.Key(), "RequirePRReviewers", a[1:])
	}}

	reviewer := &cobra.Command{Use: "reviewer", Short: "Show the managed reviewer pool, profiles, attached changes and waiting queue", Long: "Show the managed reviewer pool. Only live turns on needed current heads occupy capacity. Completed or inactive reviewers close. Queued changes wait when the pool is full. Paused rooms dispatch no reviews.", Args: cobra.NoArgs}
	roomRef := reviewer.Flags().String("room", "", "room name or ID (default: current stamped room)")
	profileName := reviewer.Flags().String("profile", "", "pin an enabled reviewer profile")
	auto := reviewer.Flags().Bool("auto", false, "choose the reviewer profile automatically")
	restart := reviewer.Flags().Bool("restart", false, "close and recreate the room reviewer pool")
	roomFor := mergeRoomFor

	mergeSettings := &cobra.Command{Use: "merge-settings", Short: "Read or set the room's maximum parallel prefix trials", Args: cobra.NoArgs}
	mergeRoom := mergeSettings.Flags().String("room", "", "room name or ID (default: current stamped room)")
	maxParallel := mergeSettings.Flags().Int("max-parallel", 3, "maximum active prefix trials")
	mergeSettings.RunE = func(c *cobra.Command, _ []string) error {
		s := o.Service()
		id, err := roomFor(s, *mergeRoom)
		if err != nil {
			return err
		}
		if c.Flags().Changed("max-parallel") {
			if err := engineclient.New("").Call(c.Context(), "SetMergeSettings", []any{id, *maxParallel}, nil); err != nil {
				return err
			}
		}
		settings, err := s.MergeSettings(id)
		if err != nil {
			return err
		}
		return output(c, settings)
	}
	root.AddCommand(mergeSettings)
	budget := &cobra.Command{Use: "budget", Short: "Read or set room CI budgets (warn 2m, fail 3m, test 10s)", Args: cobra.NoArgs}
	budgetRoom := budget.Flags().String("room", "", "room name or ID (default: current stamped room)")
	budgetWarn := budget.Flags().String("warn", "2m", "warn after this much running time")
	budgetFail := budget.Flags().String("fail", "3m", "cancel and fail after this much running time")
	budgetExempt := budget.Flags().StringSlice("exempt", nil, "workflow names exempt from timing (still gate CI); empty clears the list")
	budgetTest := budget.Flags().String("test", "10s", "maximum duration of each test")
	budgetAllow := budget.Flags().StringArray("allow", nil, "allowed slow test name or package/name (repeatable; empty clears)")
	budgetNoJUnit := budget.Flags().Bool("no-junit-warning", true, "warn once when CI uploads no JUnit report")
	budget.RunE = func(c *cobra.Command, _ []string) error {
		s := o.Service()
		roomID, err := roomFor(s, *budgetRoom)
		if err != nil {
			return err
		}
		p, err := s.RoomChangePolicy(roomID)
		if err != nil {
			return err
		}
		if c.Flags().Changed("warn") {
			p.CIWarnAfter = *budgetWarn
		}
		if c.Flags().Changed("fail") {
			p.CIFailAfter = *budgetFail
		}
		if c.Flags().Changed("exempt") {
			p.CIBudgetExempt = *budgetExempt
		}
		if c.Flags().Changed("no-junit-warning") {
			p.CINoJUnitWarning = budgetNoJUnit
		}
		if c.Flags().Changed("test") {
			p.CITestMax = *budgetTest
		}
		if c.Flags().Changed("allow") {
			p.CITestAllow = nil
			for _, name := range *budgetAllow {
				if name != "" {
					p.CITestAllow = append(p.CITestAllow, name)
				}
			}
		}
		if c.Flags().Changed("warn") || c.Flags().Changed("fail") || c.Flags().Changed("exempt") || c.Flags().Changed("test") || c.Flags().Changed("allow") || c.Flags().Changed("no-junit-warning") {
			if _, _, err = p.TestBudget(); err != nil {
				return err
			}
			if _, _, err = p.CIBudget(); err != nil {
				return err
			}
			if err = engineclient.New("").Call(c.Context(), "SetRoomChangePolicy", []any{roomID, p}, nil); err != nil {
				return err
			}
		}
		w, f, err := p.CIBudget()
		if err != nil {
			return err
		}
		max, _, err := p.TestBudget()
		if err != nil {
			return err
		}
		return output(c, map[string]any{"roomId": roomID, "warnAfter": w.String(), "failAfter": f.String(), "ciBudgetExempt": append([]string{}, p.CIBudgetExempt...), "testMax": max.String(), "testAllow": p.CITestAllow, "noJUnitWarning": p.WarnNoJUnit()})
	}

	reviewer.RunE = func(c *cobra.Command, _ []string) error {
		s := o.Service()
		roomID, err := roomFor(s, *roomRef)
		if err != nil {
			return err
		}
		if *auto && c.Flags().Changed("profile") {
			return fmt.Errorf("choose --profile or --auto")
		}
		// The engine checks the caller on both writes (requirements item 3).
		engine := engineclient.New("")
		if *auto || c.Flags().Changed("profile") {
			if err = engine.Call(c.Context(), "SetReviewerProfile", []any{roomID, *profileName}, nil); err != nil {
				return err
			}
		}
		if *restart {
			if err = engine.Call(c.Context(), "RestartReviewer", []any{roomID}, nil); err != nil {
				return err
			}
		}
		var status watch.ReviewerStatus
		if err := engine.Call(c.Context(), "ReviewerStatus", []any{roomID}, &status); err != nil {
			return err
		}
		return output(c, status)
	}
	review := &cobra.Command{Use: "review on|off|<number|url>", Short: "Turn automatic room review on or off, or publish an owner GitHub review", Long: "review on|off controls automatic room review. review <number|url> --body-file <file> [--comments-file <json-file>] [--head <sha>] publishes one GitHub comment review and its inline comments through gh, then records the review ID for routing; pass that ID to dyna git pr verdict --review-id. The comments file is a JSON array of {\"path\", \"line\", \"body\"} objects.", Args: cobra.ExactArgs(1)}
	reviewRoom := review.Flags().String("room", "", "room name or ID (default: current stamped room)")
	reviewBodyFile := review.Flags().String("body-file", "", "publish a GitHub comment review: read the Markdown review from a file (- for stdin)")
	reviewCommentsFile := review.Flags().String("comments-file", "", "JSON array of inline comments: [{\"path\": \"f.go\", \"line\": 42, \"body\": \"...\"}]")
	reviewHead := review.Flags().String("head", "", "reviewed commit SHA (default: the tracked PR head)")
	reviewPublish := review.Flags().Bool("publish", false, "explicitly publish this review to GitHub even when the room defaults to local")
	review.RunE = func(c *cobra.Command, a []string) error {
		if *reviewBodyFile != "" {
			if authority.Of(c.Context()).Role != authority.Owner {
				return fmt.Errorf("agents cannot publish GitHub PR comments or reviews; record feedback in Dyna")
			}
			s, r, err := resolve(c, a[0])
			if err != nil {
				return err
			}
			reader := c.InOrStdin()
			if *reviewBodyFile != "-" {
				file, err := os.Open(*reviewBodyFile)
				if err != nil {
					return err
				}
				defer file.Close()
				reader = file
			}
			raw, err := io.ReadAll(io.LimitReader(reader, 30001))
			if err != nil {
				return err
			}
			body := string(raw)
			var comments []watch.ReviewComment
			if *reviewCommentsFile != "" {
				data, err := os.ReadFile(*reviewCommentsFile)
				if err != nil {
					return err
				}
				if err = json.Unmarshal(data, &comments); err != nil {
					return fmt.Errorf("read inline comments JSON: %w", err)
				}
			}
			roomID, err := publicationRoom(s, r)
			if err != nil {
				return err
			}
			var id int64
			err = engineclient.New("").Call(c.Context(), "PostPRReview", []any{roomID, r, body, *reviewHead, comments, *reviewPublish}, &id)
			if err != nil {
				return err
			}
			return output(c, map[string]any{"reviewId": id})
		}
		if a[0] != "on" && a[0] != "off" {
			return fmt.Errorf("review must be on or off, or give a PR number or URL with --body-file")
		}
		s := o.Service()
		roomID, err := roomFor(s, *reviewRoom)
		if err != nil {
			return err
		}
		if err = engineclient.New("").Call(c.Context(), "SetReviewEnabled", []any{roomID, a[0] == "on"}, nil); err != nil {
			return err
		}
		status, err := s.ReviewerStatus(roomID)
		if err != nil {
			return err
		}
		return output(c, status)
	}
	publishing := &cobra.Command{Use: "publishing [local|github]", Short: "Read or set the owner's publishing policy", Long: "Agent reviews always stay in Dyna. Only the owner can select github for owner publishing. This setting preserves the reviewer role.", Args: cobra.MaximumNArgs(1)}
	publishingRoom := publishing.Flags().String("room", "", "room name or ID (default: current stamped room)")
	publishing.RunE = func(c *cobra.Command, a []string) error {
		if len(a) > 0 && a[0] == watch.PublishingGitHub && authority.Of(c.Context()).Role != authority.Owner {
			return fmt.Errorf("agent reviews stay in Dyna; use local publishing")
		}
		s := o.Service()
		ref := *publishingRoom
		if ref == "" {
			if stamp, ok := mode.StampFromEnv(); ok {
				ref = stamp.RoomID
				if ref == "" {
					ref = stamp.Room
				}
			}
		}
		rec, err := s.Notify.Rooms.Resolve(ref)
		if err != nil {
			return err
		}
		if len(a) == 1 {
			if err := engineclient.New("").Call(c.Context(), "SetReviewPublishing", []any{rec.ID, a[0]}, nil); err != nil {
				return err
			}
		}
		value, err := s.Publishing(rec.ID)
		if err != nil {
			return err
		}
		return output(c, map[string]string{"roomId": rec.ID, "reviewPublishing": value})
	}

	verdict := &cobra.Command{Use: "verdict <number|url> approve|approve-with-nits|changes|question [Markdown review]", Short: "Record a Dyna review and verdict for an exact PR head", Long: "The assigned reviewer or a session ID in the room policy reviewers list may record a verdict. Named reviewers require --head and no pool assignment. Local assignments keep the full Markdown review in Dyna and require --head; no GitHub review ID is needed. Include findings and file:line references. Use --body-file for multiline Markdown (or - for stdin), up to 12000 bytes. GitHub assignments require your published comment review and --review-id. A changes verdict sends the review to the author; approval tells the owner that the PR is ready to merge, and a separate land notice goes up the author chain. No merge is performed.", Args: cobra.RangeArgs(2, 3)}
	findingsFile := verdict.Flags().String("findings-file", "", "read structured findings from a JSON array file")
	bodyFile := verdict.Flags().String("body-file", "", "read Markdown review from a file (- for stdin), instead of a positional review")
	head := verdict.Flags().String("head", "", "exact PR head SHA (required for local and named reviews)")
	reviewID := verdict.Flags().Int64("review-id", 0, "GitHub ID of your published comment review")
	verdict.RunE = func(c *cobra.Command, a []string) error {
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		summary := ""
		if *bodyFile != "" {
			if len(a) == 3 {
				return fmt.Errorf("choose a positional review or --body-file")
			}
			reader := c.InOrStdin()
			if *bodyFile != "-" {
				file, err := os.Open(*bodyFile)
				if err != nil {
					return err
				}
				defer file.Close()
				reader = file
			}
			body, err := io.ReadAll(io.LimitReader(reader, watch.MaxReviewBytes+1))
			if err != nil {
				return err
			}
			summary = string(body)
		} else if len(a) == 3 {
			summary = a[2]
		}
		var findings []watch.Finding
		if *findingsFile != "" {
			f, err := os.Open(*findingsFile)
			if err != nil {
				return err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, watch.MaxFindingsBytes+1))
			if err != nil {
				return err
			}
			if len(data) > watch.MaxFindingsBytes {
				return fmt.Errorf("structured findings exceed %d bytes", watch.MaxFindingsBytes)
			}
			if err = json.Unmarshal(data, &findings); err != nil {
				return fmt.Errorf("read findings JSON: %w", err)
			}
		}
		rec, err := s.Store.Get(r.Key())
		if err != nil {
			return err
		}
		var result prview.VerdictResult
		if err := engineclient.New("").Call(c.Context(), "RecordPRVerdict", []any{rec.RoomID, rec.Key, *head, a[1], summary, *reviewID, findings}, &result); err != nil {
			return err
		}
		if result.Notice != "" {
			fmt.Fprintln(c.OutOrStdout(), result.Notice)
		}
		return nil
	}
	comment := &cobra.Command{Use: "comment <number|url> [body]", Short: "Post a GitHub comment through gh and record its comment ID for routing", Args: cobra.RangeArgs(1, 2)}
	commentBody := comment.Flags().String("body", "", "comment body text, as an alternative to the positional body")
	publishComment := comment.Flags().Bool("publish", false, "explicitly publish this comment to GitHub even when the room defaults to local")
	comment.RunE = func(c *cobra.Command, a []string) error {
		if authority.Of(c.Context()).Role != authority.Owner {
			return fmt.Errorf("agents cannot publish GitHub PR comments or reviews; record feedback in Dyna")
		}
		s, r, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		body := *commentBody
		if len(a) == 2 {
			if strings.TrimSpace(body) != "" {
				return fmt.Errorf("choose a positional body or --body, not both")
			}
			body = a[1]
		}
		if strings.TrimSpace(body) == "" {
			return fmt.Errorf("a comment body is required")
		}
		roomID, err := publicationRoom(s, r)
		if err != nil {
			return err
		}
		return engineclient.New("").Call(c.Context(), "CommentPR", []any{roomID, r, body, *publishComment}, nil)
	}

	noise := &cobra.Command{Use: "noise <number|url> <comment-id>", Short: "Teach a repo-scoped noise template from an obvious bot spam comment", Long: "Mark obvious bot spam from a tracked PR. Use the kind:id printed in its batch, or a numeric GitHub comment ID. The rule matches the same bot in this repo using the entire normalized text template; changed wording and appended findings stay kept. Human owner comments, inline comments, code and review findings cannot be learned. Show learned rules with dyna git pr show or noise list; remove one with noise rm <id>.", Args: cobra.ExactArgs(2)}
	noiseReason := noise.Flags().String("reason", "", "why this whole bot comment is noise")
	noise.RunE = func(c *cobra.Command, a []string) error {
		s, ref, err := resolve(c, a[0])
		if err != nil {
			return err
		}
		rec, err := s.Store.Get(ref.Key())
		if err != nil {
			return err
		}
		var result prview.LearnedPRNoise
		if err := engineclient.New("").Call(c.Context(), "LearnPRNoise", []any{rec.RoomID, ref.Key(), a[1], *noiseReason}, &result); err != nil {
			return err
		}
		return output(c, result)
	}
	noise.AddCommand(&cobra.Command{Use: "list", Short: "List learned bot noise rules and their match counts", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		rules, err := o.Service().ListNoise()
		if err != nil {
			return err
		}
		return output(c, rules)
	}}, &cobra.Command{Use: "rm <id>", Short: "Remove one learned noise rule of the current room's repositories", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		roomID, err := mergeRoomFor(o.Service(), "")
		if err != nil {
			return err
		}
		return engineclient.New("").Call(c.Context(), "RemovePRNoiseRule", []any{roomID, a[0]}, nil)
	}})
	admit := &cobra.Command{Use: "admit <number|url> <comment-id>", Short: "Owner: admit a held outside comment", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, args []string) error {
		s, ref, err := resolve(c, args[0])
		if err != nil {
			return err
		}
		rec, err := s.Store.Get(ref.Key())
		if err != nil {
			return err
		}
		var comments []prview.HeldPRComment
		if err := engineclient.New("").Call(c.Context(), "HeldPRComments", []any{rec.RoomID}, &comments); err != nil {
			return err
		}
		for _, comment := range comments {
			if comment.Key == ref.Key() && comment.ID == args[1] {
				return engineclient.New("").Call(c.Context(), "AdmitPRComment", []any{rec.RoomID, ref.Key(), args[1], comment.Version}, nil)
			}
		}
		return fmt.Errorf("comment %q is not held", args[1])
	}}
	dismiss := &cobra.Command{Use: "dismiss <number|url> <comment-id>", Short: "Owner: dismiss a held outside comment", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, args []string) error {
		s, ref, err := resolve(c, args[0])
		if err != nil {
			return err
		}
		rec, err := s.Store.Get(ref.Key())
		if err != nil {
			return err
		}
		return engineclient.New("").Call(c.Context(), "DismissPRComment", []any{rec.RoomID, ref.Key(), args[1]}, nil)
	}}
	held := &cobra.Command{Use: "held <number|url>", Short: "Owner: list held outside comments", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, ref, err := resolve(c, args[0])
		if err != nil {
			return err
		}
		rec, err := s.Store.Get(ref.Key())
		if err != nil {
			return err
		}
		var comments []prview.HeldPRComment
		if err := engineclient.New("").Call(c.Context(), "HeldPRComments", []any{rec.RoomID}, &comments); err != nil {
			return err
		}
		out := []prview.HeldPRComment{}
		for _, comment := range comments {
			if comment.Key == ref.Key() {
				out = append(out, comment)
			}
		}
		return output(c, out)
	}}
	root.AddCommand(admit, dismiss, held)
	root.AddCommand(budget, healthCommand(o, roomFor), track, show, list, untrack, transfer, resume, hold, release, require, reviewer, review, verdict, comment, publishing, noise)
	root.AddCommand(cliexport.NewCommand())
	return root
}

// prWrite changes one tracked PR through an engine call, so the engine checks
// the caller (requirements item 3) instead of this process writing the store.
func prWrite(c *cobra.Command, s *watch.Service, key, call string, args ...any) error {
	rec, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	return engineclient.New("").Call(c.Context(), call, append([]any{rec.RoomID, key}, args...), nil)
}

// publicationRoom uses the tracked room, or resolves the CLI room for an untracked PR.
func publicationRoom(s *watch.Service, ref watch.Ref) (string, error) {
	st, err := s.Store.Read()
	if err != nil {
		return "", err
	}
	if rec := st.Records[ref.Key()]; rec != nil {
		return rec.RoomID, nil
	}
	return mergeRoomFor(s, "")
}
