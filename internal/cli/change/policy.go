package change

import (
	"encoding/json"
	"fmt"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/watch"
	"github.com/spf13/cobra"
)

func policyCommand(o Options, roomFor func(*watch.Service, string) (string, error)) *cobra.Command {
	c := &cobra.Command{Use: "policy [change-id]", Short: "Show or change room policy, or override one change with a reason", Args: cobra.MaximumNArgs(1)}
	room := c.Flags().String("room", "", "room name or ID (default: current room)")
	reviewers := c.Flags().StringSlice("reviewers", nil, "exact session IDs allowed to review without a pool assignment (room policy only; empty clears)")
	budgetMode := c.Flags().String("ci-time-budget", "", "enforce or warn (room policy only)")
	github := c.Flags().String("github", "", "read-only or read-write (owner or root only)")
	var change watch.PolicyOverride
	c.Flags().StringVar(&change.Review, "review", "", "on or off")
	c.Flags().StringSliceVar(&change.RequiredChecks, "required-checks", nil, "required GitHub check names, comma-separated (empty clears the list)")
	c.Flags().StringVar(&change.Reason, "reason", "", "reason for a per-change override")
	reset := c.Flags().Bool("reset", false, "remove the per-change override")
	c.RunE = func(c *cobra.Command, a []string) error {
		for _, name := range []string{"review", "ci-time-budget"} {
			if c.Flags().Changed(name) {
				value, _ := c.Flags().GetString(name)
				if value == "" {
					return fmt.Errorf("--%s requires a value", name)
				}
			}
		}
		s := o.Service()
		engine := engineclient.New("")
		write := c.Flags().Changed("reviewers") || c.Flags().Changed("ci-time-budget") || *reset || c.Flags().Changed("review") || c.Flags().Changed("required-checks")
		if c.Flags().Changed("required-checks") && change.RequiredChecks == nil {
			change.RequiredChecks = []string{}
		}
		if change.Reason != "" && !write {
			return fmt.Errorf("--reason requires override settings")
		}
		var value any
		if len(a) == 1 {
			if c.Flags().Changed("reviewers") {
				return fmt.Errorf("--reviewers is a room policy setting")
			}
			if c.Flags().Changed("ci-time-budget") {
				return fmt.Errorf("--ci-time-budget is a room policy setting")
			}
			if c.Flags().Changed("github") {
				return fmt.Errorf("--github applies to a room, not a change")
			}
			r, err := s.Store.Get(a[0])
			if err != nil {
				return err
			}
			if *room != "" {
				id, err := roomFor(s, *room)
				if err != nil {
					return err
				}
				if id != r.RoomID {
					return fmt.Errorf("change is not in this room")
				}
			}
			if *reset {
				if !change.Empty() {
					return fmt.Errorf("choose --reset or override settings")
				}
				change = watch.PolicyOverride{}
			}
			if write {
				if err = engine.Call(c.Context(), "SetChangePolicyOverride", []any{r.RoomID, r.Key, change}, nil); err != nil {
					return err
				}
			}
			r, err = s.Store.Get(r.Key)
			if err != nil {
				return err
			}
			st, err := s.Store.Read()
			if err != nil {
				return err
			}
			value = struct {
				Policy   watch.ChangePolicy   `json:"policy"`
				Override watch.PolicyOverride `json:"override"`
			}{watch.EffectivePolicy(st, r), r.PolicyOverride}
		} else {
			if *reset || change.Reason != "" {
				return fmt.Errorf("--reset and --reason require a change ID")
			}
			id, err := roomFor(s, *room)
			if err != nil {
				return err
			}
			p, err := s.RoomChangePolicy(id)
			if err != nil {
				return err
			}
			if c.Flags().Changed("reviewers") {
				p.Reviewers = *reviewers
			}
			if c.Flags().Changed("ci-time-budget") {
				p.CITimeBudget = *budgetMode
			}
			if change.Review != "" {
				p.Review = change.Review
			}
			if change.RequiredChecks != nil {
				p.RequiredChecks = change.RequiredChecks
			}
			if write {
				if err = engine.Call(c.Context(), "SetRoomChangePolicy", []any{id, p}, nil); err != nil {
					return err
				}
			}
			if c.Flags().Changed("github") {
				if err := engine.Call(c.Context(), "SetGitHubAccess", []any{id, *github}, nil); err != nil {
					return err
				}
			}
			rec, err := s.Notify.Rooms.Resolve(id)
			if err != nil {
				return err
			}
			value = struct {
				watch.ChangePolicy
				GitHubAccess string `json:"githubAccess"`
			}{p, ghclient.Access(c.Context(), rec)}
		}
		enc := json.NewEncoder(c.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	return c
}
