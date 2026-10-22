// Package issue exposes the room issue board under dyna git.
package issue

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"github.com/spf13/cobra"
)

type Options struct{ Service func() *roomsvc.Service }

func NewCommand() *cobra.Command { return Command(Options{}) }
func Command(o Options) *cobra.Command {
	if o.Service == nil {
		o.Service = func() *roomsvc.Service { return roomsvc.New(nil) }
	}
	var roomRef, at, note string
	var asJSON bool
	root := &cobra.Command{Use: "issue <title>", Short: "File a quiet room issue for later triage", Args: cobra.ExactArgs(1)}
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "print full issue data as JSON")
	root.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (default: the current room)")
	root.Flags().StringVar(&at, "at", "", "source file and line, such as internal/parser.go:12")
	root.Flags().StringVar(&note, "note", "", "issue details")
	caller := func() (*roomsvc.Service, room.Record, string, error) {
		s := o.Service()
		r, err := s.Notify.Rooms.ResolveContext(roomRef, false)
		if err != nil {
			return s, r, "", err
		}
		snd, err := s.Notify.SelfSender()
		if err != nil {
			return s, r, "", err
		}
		if snd.Kind == notify.SenderSession {
			if snd.Session.RoomID != r.ID {
				return s, r, "", fmt.Errorf("use the current session's room")
			}
			return s, r, snd.Session.Address(), nil
		}
		if snd.Kind != notify.SenderOwner {
			return s, r, "", fmt.Errorf("issue commands need a session or the owner")
		}
		return s, r, r.Name, nil
	}
	output := func(c *cobra.Command, i room.Issue) error {
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(i)
		}
		action, link := i.State, ""
		switch i.State {
		case "open":
			action = "filed"
		case "goal":
			link = i.GoalID
		case "exported":
			link = i.GitHubURL
		}
		suffix := ""
		if link != "" {
			suffix = " -> " + link
		}
		_, err := fmt.Fprintf(c.OutOrStdout(), "%s %s : %s%s\n", action, i.ID, i.Title, suffix)
		return err
	}
	root.RunE = func(c *cobra.Command, a []string) error {
		file, line, err := location(at)
		if err != nil {
			return err
		}
		s, r, by, err := caller()
		if err != nil {
			return err
		}
		i, err := s.Notify.Rooms.FileIssue(r.ID, a[0], note, file, line, by)
		if err != nil {
			return err
		}
		return output(c, i)
	}
	var all bool
	list := &cobra.Command{Use: "list", Short: "List open room issues", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, r, _, err := caller()
		if err != nil {
			return err
		}
		rows, err := s.Issues(r.ID, all)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(rows)
		}
		for _, i := range rows {
			loc := i.File
			if i.Line > 0 {
				loc += fmt.Sprintf(":%d", i.Line)
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %s\n", i.ID, i.State, i.Title, loc)
		}
		return nil
	}}
	list.Flags().BoolVar(&all, "all", false, "include closed, goal and exported issues")
	root.AddCommand(list)
	for _, action := range []string{"close", "goal", "export"} {
		var value string
		c := &cobra.Command{Use: action + " <id>", Short: action + " an open issue (root or owner)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
			if action == "close" {
				if len(a) == 2 {
					if c.Flags().Changed("reason") && strings.TrimSpace(value) != strings.TrimSpace(a[1]) {
						return fmt.Errorf("positional reason and --reason disagree")
					}
					value = a[1]
				}
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("provide a close reason as a second argument or --reason")
				}
			}
			s, r, by, err := caller()
			if err != nil {
				return err
			}
			i, err := s.TriageIssue(c.Context(), r.ID, a[0], action, value, by)
			if err != nil {
				return err
			}
			return output(c, i)
		}}
		switch action {
		case "close":
			c.Use = "close <id> [reason]"
			c.Args = cobra.RangeArgs(1, 2)
			c.Flags().StringVar(&value, "reason", "", "reason for closing (or supply a second argument)")
		case "goal":
			c.Flags().StringVar(&value, "for", "", "goal holder, such as @name (default: shared room goal)")
		}
		root.AddCommand(c)
	}
	return root
}

func location(at string) (string, int, error) {
	if at == "" {
		return "", 0, nil
	}
	n := strings.LastIndex(at, ":")
	if n < 1 {
		return "", 0, fmt.Errorf("--at needs file:line")
	}
	line, err := strconv.Atoi(at[n+1:])
	if err != nil || line < 1 {
		return "", 0, fmt.Errorf("--at needs a positive line number")
	}
	return at[:n], line, nil
}
