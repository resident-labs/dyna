package ci

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
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
	run := &cobra.Command{Use: "run [sha]", Short: "Run CI on one commit using a registered node", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
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
		definition, err := localci.LoadDefinition(c.Context(), checkout.Root, sha)
		if errors.Is(err, localci.ErrDefinitionNotFound) {
			fmt.Fprintf(c.OutOrStdout(), "Local CI is not configured at %s; hosted CI remains unchanged.\n", sha)
			return nil
		}
		if err != nil {
			return err
		}
		requested, err := definition.ParseSuites(suites)
		if err != nil {
			return err
		}
		runner := localci.Runner{}
		nodes, err := (localci.Store{}).Nodes(c.Context())
		if err != nil {
			return err
		}
		var node localci.Node
		if nodeName == "" {
			node, err = defaultNode(nodes)
			if err != nil {
				return err
			}
		} else {
			found, ok := findNode(nodes, nodeName)
			if !ok {
				return fmt.Errorf("enabled CI node %q was not found", nodeName)
			}
			node = found
		}
		runner.Node = &node
		// A JSON result stays machine readable, so the choice goes to stderr.
		message := c.OutOrStdout()
		if asJSON {
			message = c.ErrOrStderr()
		}
		fmt.Fprintf(message, "CI machine: %s\n", node.Name)
		runner.OnCacheRepair = svc.CacheRepairReporter(rec.ID)
		result, runErr := runner.Run(c.Context(), localci.Request{Definition: definition, Repo: checkout.Root, SHA: sha, Suites: requested, Timeout: timeout, MaxTest: max, Allowed: allowed})
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
	run.Flags().StringSliceVar(&suites, "suite", nil, "suite from .dyna/local-ci.yaml (repeatable; default: full workflow)")
	run.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "timeout for each check")
	run.Flags().StringVar(&nodeName, "node", "", "registered CI machine to use (default: the only enabled machine)")
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

// findNode picks the enabled machine a manual run may use. The name "local"
// also selects this machine, the row with no SSH destination, so scripts that
// named the old local node keep working.
func findNode(nodes []localci.Node, name string) (localci.Node, bool) {
	for _, node := range nodes {
		if node.Name == name {
			return node, node.Enabled
		}
	}
	if name == "local" {
		for _, node := range nodes {
			if node.SSH == "" {
				return node, node.Enabled
			}
		}
	}
	return localci.Node{}, false
}

// defaultNode picks the machine a manual run uses when --node is absent. One
// enabled machine needs no choice; with several, the owner names one.
func defaultNode(nodes []localci.Node) (localci.Node, error) {
	enabled := []localci.Node{}
	for _, node := range nodes {
		if node.Enabled {
			enabled = append(enabled, node)
		}
	}
	switch len(enabled) {
	case 0:
		return localci.Node{}, fmt.Errorf("no enabled CI machine is registered; add one with dyna machine add")
	case 1:
		return enabled[0], nil
	}
	names := make([]string, len(enabled))
	for i, node := range enabled {
		names[i] = node.Name
	}
	sort.Strings(names)
	return localci.Node{}, fmt.Errorf("several enabled CI machines are registered; choose one with --node (%s)", strings.Join(names, ", "))
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
		name := string(suite.Name())
		if suite.Node != "" {
			name += " on " + suite.Node
		}
		fmt.Fprintf(c.OutOrStdout(), "  %s: %s (%s)\n", name, suite.Status, suite.Duration.Round(time.Millisecond))
		if label := suite.Cause.Label(); label != "" {
			fmt.Fprintf(c.OutOrStdout(), "  cause: %s\n", label)
		}
		if suite.Note != "" {
			fmt.Fprintf(c.OutOrStdout(), "  note: %s\n", suite.Note)
		}
		if suite.Error != "" {
			fmt.Fprintln(c.OutOrStdout(), suite.Error)
		}
		if suite.Status != "passed" && suite.LogTail != "" {
			fmt.Fprintln(c.OutOrStdout(), suite.LogTail)
		}
	}
	if result.Error != "" {
		if label := result.Cause.Label(); label != "" {
			fmt.Fprintf(c.OutOrStdout(), "  cause: %s\n", label)
		}
		fmt.Fprintln(c.OutOrStdout(), result.Error)
	}
	return nil
}
