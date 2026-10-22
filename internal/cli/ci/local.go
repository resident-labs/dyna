package ci

import (
	"fmt"
	"os"
	"time"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/repoid"
	"github.com/spf13/cobra"
)

func localCommands() []*cobra.Command {
	var suites []string
	var asJSON bool
	var timeout time.Duration
	var nodeName string
	run := &cobra.Command{Use: "run [sha]", Short: "Run CI on one commit using a registered node or SSH", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		repo, err := os.Getwd()
		if err != nil {
			return err
		}
		// Resolve the checkout before reading the room policy or starting remote work.
		checkout, err := repoid.Resolve(c.Context(), repo)
		if err != nil {
			return err
		}
		svc := watch.New()
		rec, err := svc.Notify.Rooms.ResolveContext("", false)
		if err != nil {
			return err
		}
		policy, err := svc.RoomChangePolicy(rec.ID)
		if err != nil {
			return err
		}
		max, allowed, err := policy.TestBudget()
		if err != nil {
			return err
		}
		sha := "HEAD"
		if len(args) > 0 {
			sha = args[0]
		}
		runner := localci.Runner{}
		if nodeName != "" {
			nodes, err := (localci.Store{}).Nodes(c.Context())
			if err != nil {
				return err
			}
			for _, node := range nodes {
				if node.Name == nodeName && node.Enabled {
					runner.Node = &node
					break
				}
			}
			if runner.Node == nil {
				return fmt.Errorf("enabled CI node %q was not found", nodeName)
			}
		}
		result, runErr := runner.Run(c.Context(), localci.Request{Repo: checkout.Root, SHA: sha, Suites: suites, Timeout: timeout, MaxTest: max, Allowed: allowed})
		if result.SHA != "" {
			if err := printResult(c, result, asJSON); err != nil {
				return err
			}
		}
		if runErr != nil {
			return runErr
		}
		if result.Status != "passed" {
			return fmt.Errorf("local CI %s", result.Status)
		}
		return nil
	}}
	run.Flags().StringSliceVar(&suites, "suite", nil, "suite to run: relay, frontend, go, cross-build (repeatable; default: all)")
	run.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "timeout for each suite")
	run.Flags().StringVar(&nodeName, "node", "", "registered node to use instead of localci.remote")
	run.Flags().BoolVar(&asJSON, "json", false, "print the saved result as JSON")
	var showJSON bool
	show := &cobra.Command{Use: "show <sha>", Short: "Show saved local CI results for a commit", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		repo, err := os.Getwd()
		if err != nil {
			return err
		}
		sha, err := repoid.Git(c.Context(), repo, "rev-parse", "--verify", "--end-of-options", args[0]+"^{commit}")
		if err != nil {
			return err
		}
		result, err := (localci.Runner{}).Show(repo, sha)
		if err != nil {
			return err
		}
		return printResult(c, result, showJSON)
	}}
	show.Flags().BoolVar(&showJSON, "json", false, "print the saved result as JSON")
	return []*cobra.Command{run, show}
}
func printResult(c *cobra.Command, result localci.Result, asJSON bool) error {
	if asJSON {
		return jsonout.Write(c.OutOrStdout(), result)
	}
	fmt.Fprintf(c.OutOrStdout(), "%s %s (%s)\n", result.SHA, result.Status, result.Duration.Round(time.Millisecond))
	checks := result.Suites
	if len(result.Checks) > 0 {
		checks = result.Checks
	}
	for _, suite := range checks {
		name := suite.CheckName
		if name == "" {
			name = suite.Suite
		}
		if suite.Node != "" {
			name += " on " + suite.Node
		}
		fmt.Fprintf(c.OutOrStdout(), "  %s: %s (%s)\n", name, suite.Status, suite.Duration.Round(time.Millisecond))
		if suite.Error != "" {
			fmt.Fprintln(c.OutOrStdout(), suite.Error)
		}
		if suite.Status != "passed" && suite.LogTail != "" {
			fmt.Fprintln(c.OutOrStdout(), suite.LogTail)
		}
	}
	if result.Error != "" {
		fmt.Fprintln(c.OutOrStdout(), result.Error)
	}
	return nil
}
