// Package issue exposes the room issue board as `dyna issue`.
package issue

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"github.com/spf13/cobra"
)

type Options struct{ Service func() *roomsvc.Service }

// shownIssue is one issue with the note it was filed with, for --json.
type shownIssue struct {
	room.Issue
	Note string `json:"note,omitempty"`
}

func NewCommand() *cobra.Command { return Command(Options{}) }
func Command(o Options) *cobra.Command {
	if o.Service == nil {
		o.Service = func() *roomsvc.Service { return roomsvc.New(nil) }
	}
	var roomRef, at, note string
	var asJSON bool
	root := &cobra.Command{Use: "issue <title|D-N>", Short: "File a quiet room issue for later triage", Long: "File a quiet room issue for later triage. Issues have room numbers such as D-12. Triage commands accept D-12, d12 or the original message ID. A lone issue reference reads that issue instead of filing one; dyna issue show D-12 does the same.", Args: cobra.ExactArgs(1)}
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
		case room.IssueStateOpen:
			action = "filed"
		case room.IssueStateGoal:
			link = i.GoalID
		case room.IssueStateExported:
			link = i.GitHubURL
		}
		suffix := ""
		if link != "" {
			suffix = " -> " + link
		}
		_, err := fmt.Fprintf(c.OutOrStdout(), "%s %s : %s%s\n", action, i.Label(), i.Title, suffix)
		return err
	}
	// show prints one issue as the board shows it: its state and title, its
	// origin and link, and the note the filer gave it.
	show := func(c *cobra.Command, ref string) error {
		s, r, _, err := caller()
		if err != nil {
			return err
		}
		i, note, err := s.Issue(r.ID, ref)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(shownIssue{Issue: i, Note: note})
		}
		out := c.OutOrStdout()
		fmt.Fprintf(out, "%s  %s  %s\n", i.Label(), i.State, i.Title)
		meta := []string{}
		if i.Filer != "" {
			meta = append(meta, "from "+i.Filer)
		}
		if i.FiledAt > 0 {
			meta = append(meta, time.UnixMilli(i.FiledAt).Local().Format("2006-01-02 15:04"))
		}
		if i.File != "" {
			loc := i.File
			if i.Line > 0 {
				loc += fmt.Sprintf(":%d", i.Line)
			}
			meta = append(meta, loc)
		}
		if i.GoalID != "" {
			meta = append(meta, "goal "+i.GoalID)
		}
		if i.GitHubURL != "" {
			meta = append(meta, i.GitHubURL)
		}
		if i.Reason != "" {
			meta = append(meta, "reason: "+i.Reason)
		}
		if len(meta) > 0 {
			fmt.Fprintln(out, strings.Join(meta, " · "))
		}
		if note != "" {
			fmt.Fprintf(out, "\n%s\n", note)
		}
		return nil
	}
	root.RunE = func(c *cobra.Command, a []string) error {
		title := strings.TrimSpace(a[0])
		// A lone issue reference names an issue to read, never a new title. An
		// agent that meant to read D-61 filed an empty issue titled "D-61".
		if room.IsIssueNumber(title) || strings.HasPrefix(title, "msg_") {
			if c.Flags().Changed("note") || c.Flags().Changed("at") {
				return fmt.Errorf("%s reads an issue; --note and --at belong to a new issue", title)
			}
			return show(c, title)
		}
		file, line, err := location(at)
		if err != nil {
			return err
		}
		s, r, by, err := caller()
		if err != nil {
			return err
		}
		i, err := s.Notify.Rooms.FileIssue(r.ID, room.IssueInput{Title: a[0], Note: note, File: file, Line: line, Filer: by})
		if err != nil {
			return err
		}
		return output(c, i)
	}
	root.AddCommand(&cobra.Command{Use: "show <issue>", Short: "Show one issue by D-N or message ID", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		return show(c, a[0])
	}})
	var all bool
	list := &cobra.Command{Use: "list", Short: "List unhandled room issues", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
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
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %s\n", i.Label(), i.State, i.Title, loc)
		}
		return nil
	}}
	list.Flags().BoolVar(&all, "all", false, "include in-progress, closed, goal and exported issues")
	root.AddCommand(list)
	for _, action := range []string{"close", "export"} {
		var value string
		c := &cobra.Command{Use: action + " <issue>", Short: action + " an open issue by D-N or message ID (root or owner)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
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
			c.Use = "close <issue> [reason]"
			c.Args = cobra.RangeArgs(1, 2)
			c.Flags().StringVar(&value, "reason", "", "reason for closing (or supply a second argument)")
		case "goal":
			c.Flags().StringVar(&value, "for", "", "goal holder, such as @name (default: shared room goal)")
		}
		root.AddCommand(c)
	}
	// TODO: legacy/shim remove after one release — the hidden alias teaches
	// the single goal-creation command instead of creating a second kind of goal.
	legacyFor := ""
	legacyGoal := &cobra.Command{Use: "goal <issue>", Short: "replaced by dyna goal --fixes", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if legacyFor != "" {
			_, err := fmt.Fprintf(c.OutOrStdout(), "dyna goal --for %s --fixes %s\n", legacyFor, a[0])
			return err
		}
		_, err := fmt.Fprintf(c.OutOrStdout(), "dyna goal --fixes %s\n", a[0])
		return err
	}}
	legacyGoal.Flags().StringVar(&legacyFor, "for", "", "ignored holder; use dyna goal --for @name")
	root.AddCommand(legacyGoal)
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
