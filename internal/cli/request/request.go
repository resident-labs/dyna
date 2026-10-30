// Package request files and manages requests to the owner or to a parent
// session: a terminal step that only a person can do.
package request

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"github.com/spf13/cobra"
)

var engineClient = func() *engineclient.Client { return engineclient.New("") }

// NewCommand is the dyna request group.
func NewCommand() *cobra.Command {
	var roomRef string
	var jsonOut bool
	cmd := &cobra.Command{Use: "request", Short: "Ask for a terminal step that only a person can do"}
	cmd.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (defaults to your room)")
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "print JSON")

	var reason, cwd string
	var noCommand bool
	terminal := &cobra.Command{
		Use:   "terminal --reason <why> [--cwd DIR] [--no-command] -- <command> [args...]",
		Short: "Ask the owner to run a command in a terminal",
		Long: "Ask the owner to run a command that needs a person, such as a sign-in or sudo. " +
			"The request goes up your parent chain first; each parent approves or denies it, and the root's approval shows it to the owner. " +
			"The owner reads the exact command, runs it in a terminal and reports done or failed. " +
			"You get the verdict, the exit code and the owner's note; you never see the screen unless the owner shares lines. " +
			"This command returns at once; the result arrives as a post. You have one open request: a new one replaces the older.",
		RunE: func(c *cobra.Command, args []string) error {
			if noCommand == (len(args) > 0) {
				return fmt.Errorf("give a command after --, or --no-command for a plain login shell")
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required: say why a person must do this")
			}
			dir, err := folder(cwd)
			if err != nil {
				return err
			}
			s := notify.Default()
			snd, err := s.SelfSender()
			if err != nil {
				return err
			}
			rec, err := s.QuestionRoom(snd, roomRef)
			if err != nil {
				return err
			}
			var q questions.Question
			err = engineClient().Call(c.Context(), "FileTerminalRequest", []any{notify.TerminalRequestInput{Room: rec.ID, Argv: args, Cwd: dir, Reason: reason, Claim: notify.ClaimOf(snd)}}, &q)
			if q.ID != "" {
				if jsonOut {
					_ = json.NewEncoder(c.OutOrStdout()).Encode(q)
				} else {
					fmt.Fprintf(c.OutOrStdout(), "Filed %s (%s). Keep working; the result arrives as a post.\n", q.ID, stageWords(q))
				}
			}
			return err
		},
	}
	terminal.Flags().StringVar(&reason, "reason", "", "why a person must run this (required, at most 500 characters)")
	terminal.Flags().StringVar(&cwd, "cwd", "", "folder to run in (default: your working directory)")
	terminal.Flags().BoolVar(&noCommand, "no-command", false, "ask for a plain login shell in the folder, with no command")

	list := &cobra.Command{Use: "list", Short: "List your requests and the requests you hold", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s := notify.Default()
		snd, err := s.SelfSender()
		if err != nil {
			return err
		}
		return listMine(c, s, snd, roomRef, jsonOut)
	}}

	approve := &cobra.Command{Use: "approve <id>", Short: "Approve an approval or pass a terminal request to your parent", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if approvalID(args[0]) {
			if err := decideApproval(c, roomRef, args[0], approvals.ActionApprove, ""); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Approved %s.\n", args[0])
			return nil
		}
		if err := call(c, roomRef, "ApproveRequest", func(ref string) []any { return []any{ref, args[0]} }); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "Passed request %s up to the next holder.\n", args[0])
		return nil
	}}
	var why, approvalWhy string
	deny := &cobra.Command{Use: "deny <id> --reason <why>", Short: "Refuse a request you hold; the filer reads your reason", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if approvalID(args[0]) {
			if strings.TrimSpace(approvalWhy) == "" {
				return fmt.Errorf("--why is required when denying an approval")
			}
			if err := decideApproval(c, roomRef, args[0], approvals.ActionDeny, approvalWhy); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Denied %s.\n", args[0])
			return nil
		}
		if err := call(c, roomRef, "DenyRequest", func(ref string) []any { return []any{ref, args[0], why} }); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "Denied request %s.\n", args[0])
		return nil
	}}
	deny.Flags().StringVar(&why, "reason", "", "why you refuse a terminal request (required)")
	deny.Flags().StringVar(&approvalWhy, "why", "", "why you refuse an approval (required)")
	var withdrawWhy string
	withdraw := &cobra.Command{Use: "withdraw <id>", Short: "Withdraw a request", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return call(c, roomRef, "WithdrawRequest", func(ref string) []any { return []any{ref, args[0], withdrawWhy} })
	}}
	withdraw.Flags().StringVar(&withdrawWhy, "why", "", "why the request no longer matters")
	var passWhy string
	pass := &cobra.Command{Use: "pass A-<n>", Short: "Pass an approval to your parent, or from root to the owner", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return decideApproval(c, roomRef, args[0], approvals.ActionPass, passWhy)
	}}
	pass.Flags().StringVar(&passWhy, "why", "", "why the next holder should decide")
	cancel := &cobra.Command{Use: "cancel A-<n>", Short: "Cancel your pending approval request", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return call(c, roomRef, "ApprovalCancel", func(ref string) []any { return []any{ref, args[0]} })
	}}
	cmd.AddCommand(terminal, newQuestion(&roomRef, &jsonOut), newAnswer(&roomRef), list, approve, deny, pass, cancel, withdraw)
	return cmd
}

// listMine prints the requests the caller filed and the ones it holds.
func listMine(c *cobra.Command, s *notify.Service, snd notify.Sender, roomRef string, jsonOut bool) error {
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	qs, err := s.QuestionStore().List(rec.ID)
	if err != nil {
		return err
	}
	me := "owner"
	if snd.Kind == notify.SenderSession {
		me = snd.Session.ID
	}
	out := []questions.Question{}
	for _, q := range qs {
		if mine(q, me) {
			out = append(out, q)
		}
	}
	if jsonOut {
		return json.NewEncoder(c.OutOrStdout()).Encode(out)
	}
	for _, q := range out {
		role := "yours"
		if q.Asker != me {
			role = "held by you, filed by " + q.Asker
		}
		fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %s", q.ID, role, stageWords(q), q.Text)
		if q.Answer != nil && !q.IsTerminal() {
			fmt.Fprintf(c.OutOrStdout(), " — %s %s", q.Answer.Choice, q.Answer.Text)
		}
		fmt.Fprintln(c.OutOrStdout())
	}
	return nil
}

// mine reports a request the caller filed that is open or ended within a day,
// or an open request the caller holds.
func mine(q questions.Question, me string) bool {
	if q.Asker == me {
		return q.State == "open" || q.Answer != nil && time.Since(q.Answer.At) <= 24*time.Hour
	}
	return q.State == "open" && q.Terminal != nil && q.Terminal.Holder == me
}

func stageWords(q questions.Question) string {
	if q.IsTerminal() {
		return string(q.TerminalStage())
	}
	return q.State
}

func folder(cwd string) (string, error) {
	if cwd == "" {
		return os.Getwd()
	}
	return filepath.Abs(cwd)
}

func call(c *cobra.Command, roomRef, method string, args func(roomID string) []any) error {
	s := notify.Default()
	snd, err := s.SelfSender()
	if err != nil {
		return err
	}
	rec, err := s.QuestionRoom(snd, roomRef)
	if err != nil {
		return err
	}
	return engineClient().Call(c.Context(), method, args(rec.ID), nil)
}
