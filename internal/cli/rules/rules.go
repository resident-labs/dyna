// Package rules exposes standing room rules to agents and the owner.
package rules

import (
	"encoding/json"
	"fmt"
	"time"

	"dyna-agent/internal/cli/goal"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/room"
	"github.com/spf13/cobra"
)

type Options struct {
	Service func() *goalsvc.Service
	Client  *engineclient.Client
}

func NewCommand() *cobra.Command { return Command(Options{}) }
func Command(o Options) *cobra.Command {
	if o.Service == nil {
		o.Service = goalsvc.New
	}
	if o.Client == nil {
		o.Client = engineclient.New("")
	}
	var roomRef string
	var asJSON, global bool
	root := &cobra.Command{Use: "rules", Short: "List and change standing room rules", Long: "List and change standing room rules. With --global, list and change global rules, which apply in every room and reach each harness through its native instruction file. A global change takes effect when a harness next starts, and its notice goes only to the current room. Room rules win over global rules. Agents may add rules and change agent-written rules. Only the owner may change owner rules. The engine checks the caller identity and protects owner rules. Each open agent reads a room rule change as an instructions note on its next turn, so do not repost the rule to it.", Args: cobra.NoArgs}
	root.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (default: the stamped room, else the current repo's room)")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "print JSON")
	root.PersistentFlags().BoolVar(&global, "global", false, "use global rules, which apply in every room")
	scope := func(rec room.Record) string {
		if global {
			return goals.Global
		}
		return rec.ID
	}
	list := func(c *cobra.Command, _ []string) error {
		s, rec, _, err := goal.Caller(o.Service(), roomRef)
		if err != nil {
			return err
		}
		v, err := s.Store.RulesView(scope(rec))
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(v)
		}
		for _, rule := range v.Rules {
			who := rule.SetterName
			if rule.Setter == "owner" {
				who = "owner"
			} else if who == "" {
				who = rule.Setter
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %s\n", rule.ID, who, age(rule.UpdatedAt), rule.Text)
		}
		return nil
	}
	root.RunE = list
	root.AddCommand(&cobra.Command{Use: "list", Short: "List room rules", Args: cobra.NoArgs, RunE: list})
	for _, verb := range []string{"add", "edit", "rm"} {
		use, short, count := "add <text>", "Add a standing rule", 1
		if verb == "edit" {
			use, short, count = "edit <id> <text>", "Edit a standing rule", 2
		}
		if verb == "rm" {
			use, short = "rm <id>", "Remove a standing rule"
		}
		root.AddCommand(&cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count), RunE: func(c *cobra.Command, a []string) error {
			args := []any{goal.MutationRoom(roomRef), global}
			method := "AddRule"
			if verb == "edit" {
				method = "EditRule"
			}
			if verb == "rm" {
				method = "RemoveRule"
			}
			for _, arg := range a {
				args = append(args, arg)
			}
			var rule goals.Rule
			if err := o.Client.Call(c.Context(), method, args, &rule); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(c.OutOrStdout()).Encode(rule)
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s\n", rule.ID, rule.Text)
			return nil
		}})
	}
	return root
}

func age(at time.Time) string {
	d := time.Since(at)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
