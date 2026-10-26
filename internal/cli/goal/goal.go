// Package goal exposes room goals to agents and the owner.
package goal

import (
	"context"
	"dyna-agent/internal/roomsvc"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/mode"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"github.com/spf13/cobra"
)

type Options struct {
	Service func() *goalsvc.Service
	Client  *engineclient.Client
}

func NewCommand() *cobra.Command { return Command(Options{}) }

func (o Options) withDefaults() Options {
	if o.Service == nil {
		o.Service = goalsvc.New
	}
	if o.Client == nil {
		o.Client = engineclient.New("")
	}
	return o
}

// Note appends text to a goal's journal through the engine.
func Note(ctx context.Context, o Options, roomRef, id, text string) (goals.Event, error) {
	var e goals.Event
	err := o.withDefaults().Client.Call(ctx, "NoteGoal", []any{MutationRoom(roomRef), id, text}, &e)
	return e, err
}

// OpenHeldGoals returns the IDs of the open goals the calling session holds.
func OpenHeldGoals(o Options) ([]string, error) {
	s, rec, by, err := Caller(o.withDefaults().Service(), "")
	if err != nil {
		return nil, err
	}
	records, err := s.Store.List(rec.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range records {
		if g.Open() && g.HeldBy(by) {
			ids = append(ids, g.ID)
		}
	}
	return ids, nil
}

func Command(o Options) *cobra.Command {
	o = o.withDefaults()
	var roomRef, fixesText string
	var all, asJSON bool
	var selectors []string
	root := &cobra.Command{Use: "goal [plain English text]", Args: cobra.MaximumNArgs(1), Short: "Set, hold and finish long-lasting room goals"}
	root.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (default: the stamped room or the current repo's room)")
	root.Flags().BoolVar(&all, "all", false, "include every goal in the room, including done goals")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "print full goal data as JSON")
	root.Flags().StringArrayVar(&selectors, "for", nil, "holder selector, repeatable: @name, @children, @siblings or @room")
	root.Flags().StringVar(&fixesText, "fixes", "", "room issues linked to this goal, such as D-35,D-40")

	listGoals := func(c *cobra.Command, _ []string) error {
		s, rec, by, err := Caller(o.Service(), roomRef)
		if err != nil {
			return err
		}
		records, err := s.Store.List(rec.ID)
		if err != nil {
			return err
		}
		counts, err := s.Store.ItemCounts(rec.ID)
		if err != nil {
			return err
		}
		// Only the holders of listed goals need a name. Listing every
		// session, closed ones included, read and parsed all their records.
		names := map[string]string{"owner": "owner"}
		nameOf := func(id string) string {
			if _, seen := names[id]; !seen {
				info, _ := s.Notify.Sessions.Store.Inspect(id)
				names[id] = info.Name
			}
			return names[id]
		}
		var yours, others []goals.Goal
		for _, g := range records {
			if !all && !g.Open() {
				continue
			}
			if g.Concerns(by) {
				yours = append(yours, g)
			} else {
				others = append(others, g)
			}
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(struct {
				Yours  []goals.Goal `json:"yours"`
				Others []goals.Goal `json:"others"`
			}{append([]goals.Goal{}, yours...), append([]goals.Goal{}, others...)})
		}
		line := func(g goals.Goal) {
			holders := []string{}
			for _, id := range g.Holders {
				name := nameOf(id)
				if name == "" {
					name = id
				}
				holders = append(holders, name)
			}
			if g.Shared {
				holders = []string{"room"}
			}
			// An old build left this goal with the owner, and only root can move it.
			if g.NeedsRoot() {
				holders = []string{"nobody; root asks the owner, then drops or finishes the goal"}
			}
			items := ""
			if n := counts[g.ID]; n[1] > 0 {
				items = fmt.Sprintf(" · %d/%d items", n[0], n[1])
			}
			setter := nameOf(g.Setter)
			if setter == "" {
				setter = g.Setter
			}
			fmt.Fprintf(c.OutOrStdout(), "%s ◎ %s · held by %s · from %s · %s%s\n", g.ID, goals.Clip(g.Text, 160), strings.Join(holders, ", "), setter, g.State, items+expediteNote(g))
		}
		for _, section := range []struct {
			title string
			goals []goals.Goal
		}{{"Your goals", yours}, {"Other open goals in this room", others}} {
			fmt.Fprintf(c.OutOrStdout(), "%s:\n", section.title)
			if len(section.goals) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "  none")
			}
			for _, g := range section.goals {
				line(g)
			}
		}
		return nil
	}
	root.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 0 && !c.Flags().Changed("fixes") {
			return listGoals(c, args)
		}
		if len(args) == 1 && isGoalCommandWord(root, args[0]) {
			return fmt.Errorf("%q looks like a goal command; use 'dyna goal list' to list goals, 'dyna goal --help' for commands, or write a descriptive goal sentence", strings.TrimSpace(args[0]))
		}
		var fixes []int
		if c.Flags().Changed("fixes") {
			var err error
			if fixes, err = prstate.ParseDynaIssueNumbers(fixesText); err != nil {
				return err
			}
			if len(fixes) == 0 {
				return fmt.Errorf("--fixes needs at least one room issue such as D-35")
			}
		}
		text := ""
		if len(args) == 1 {
			text = args[0]
		}
		var g goals.Goal
		err := o.Client.Call(c.Context(), "AddGoal", []any{MutationRoom(roomRef), text, selectors, fixes}, &g)
		if err != nil {
			return err
		}
		return printGoal(c, g)
	}
	list := &cobra.Command{Use: "list", Short: "List room goals", Args: cobra.NoArgs, RunE: listGoals}
	list.Flags().BoolVar(&all, "all", false, "include every goal in the room, including done goals")

	show := &cobra.Command{Use: "show <id>", Short: "Show goal status; use --json for full goal history", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, _, err := Caller(o.Service(), roomRef)
		if err != nil {
			return err
		}
		g, err := s.Store.Get(r.ID, a[0])
		if err != nil {
			return err
		}
		items, err := s.Store.Items(r.ID, a[0])
		if err != nil {
			return err
		}
		status, err := s.GoalStatus(r.ID, a[0])
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(struct {
				goals.Goal
				Status goals.Status `json:"status"`
				Items  []goals.Item `json:"items"`
			}{g, status, items})
		}
		if _, err = fmt.Fprintf(c.OutOrStdout(), "%s %s · %s — %s\n", g.ID, g.State, status.Label, status.Reason); err != nil {
			return err
		}
		for _, it := range items {
			mark := " "
			if it.State != goals.ItemOpen {
				mark = it.State[:1]
			}
			line := strings.SplitN(it.Text, "\n", 2)[0]
			who := ""
			if it.Assignee != "" {
				who = " · @" + it.Assignee
			}
			if _, err = fmt.Fprintf(c.OutOrStdout(), "  [%s] %s %s%s\n", mark, it.ID, goals.Clip(line, 120), who); err != nil {
				return err
			}
		}
		return nil
	}}
	edit := &cobra.Command{Use: "edit <id> [new text]", Short: "Edit a goal's text, holders or issue links and reopen it; --amend corrects its brief", Long: "Changes a goal's text, replaces its holders or links room issues, and reopens the goal. Use --amend \"<text>\" to append a correction to the goal's brief instead: the brief and the goal text stay, the holder reads the correction, and the goal screen shows it. Only the owner or root may amend, and --amend takes no other change.", Args: cobra.RangeArgs(1, 2)}
	var editFor []string
	var editFixes string
	var editAmend string
	edit.Flags().StringArrayVar(&editFor, "for", nil, "replace holders with these selectors (repeatable)")
	edit.Flags().StringVar(&editFixes, "fixes", "", "room issues linked to this existing goal, such as D-35,D-40")
	edit.Flags().StringVar(&editAmend, "amend", "", "append a correction to the goal's brief; the goal text and holders stay")
	edit.RunE = func(c *cobra.Command, a []string) error {
		if c.Flags().Changed("amend") {
			if len(a) == 2 || c.Flags().Changed("for") || c.Flags().Changed("fixes") {
				return fmt.Errorf("--amend corrects the brief alone; use it without new text, --for or --fixes")
			}
			var g goals.Goal
			if err := o.Client.Call(c.Context(), "AmendGoalBrief", []any{MutationRoom(roomRef), a[0], editAmend}, &g); err != nil {
				return err
			}
			return printGoal(c, g)
		}
		var selected *[]string
		if c.Flags().Changed("for") {
			selected = &editFor
		}
		text := ""
		if len(a) == 2 {
			text = a[1]
		}
		var fixes []int
		if c.Flags().Changed("fixes") {
			var err error
			if fixes, err = prstate.ParseDynaIssueNumbers(editFixes); err != nil {
				return err
			}
			if len(fixes) == 0 {
				return fmt.Errorf("--fixes needs at least one room issue such as D-35")
			}
		}
		var g goals.Goal
		err := o.Client.Call(c.Context(), "EditGoalSelectors", []any{MutationRoom(roomRef), a[0], text, selected, fixes}, &g)
		if err != nil {
			return err
		}
		return printGoal(c, g)
	}
	finish := func(drop bool) *cobra.Command {
		var closes []string
		use, short := "done <id> <summary>", "Mark a goal done"
		long := "Ends a goal its holder owns. A session that holds one item of a goal finishes the item with dyna goal item done and leaves the goal open; its holder, root or the owner ends the goal."
		args := cobra.ExactArgs(2)
		if drop {
			use, short, args = "drop <id> [why]", "Drop a goal", cobra.RangeArgs(1, 2)
			long = "Cancels a goal its holder owns, with the same holders that may end it."
		}
		cmd := &cobra.Command{Use: use, Short: short, Long: long, Args: args, RunE: func(c *cobra.Command, a []string) error {
			summary := ""
			if len(a) == 2 {
				summary = a[1]
			}
			var g roomsvc.FinishedGoal
			err := o.Client.Call(c.Context(), "FinishGoal", []any{MutationRoom(roomRef), a[0], summary, drop, closes}, &g)
			if err != nil {
				return err
			}
			if asJSON, _ := c.Flags().GetBool("json"); asJSON {
				return json.NewEncoder(c.OutOrStdout()).Encode(g)
			}
			if err := printGoal(c, g.Goal); err != nil {
				return err
			}
			if len(g.LeftOpen) > 0 {
				_, err = fmt.Fprintf(c.OutOrStdout(), "left open: %s (close one with dyna goal done --close D-N)\n", strings.Join(g.LeftOpen, ", "))
				return err
			}
			return nil
		}}
		if !drop {
			cmd.Flags().StringSliceVar(&closes, "close", nil, "close this linked issue, such as D-5; repeat for each. A merged PR closes the issues it fixes, and the rest stay open")
		}
		return cmd
	}
	expedite := &cobra.Command{Use: "expedite [id]", Short: "Owner or root: put one goal first in the room's review and merge queues", Long: "Marks the goal's agents and changes for earlier review and merge. Each room has at most one expedited goal. It lasts until the goal is done or dropped, until --off, or for 24 hours. Use --replace to switch from another goal.", Args: cobra.MaximumNArgs(1)}
	replace := expedite.Flags().Bool("replace", false, "replace the room's current expedited goal")
	off := expedite.Flags().Bool("off", false, "stop the room's expedited goal")
	expedite.RunE = func(c *cobra.Command, a []string) error {
		if *off == (len(a) == 1) {
			return fmt.Errorf("give a goal ID, or --off with no ID")
		}
		id := ""
		if len(a) == 1 {
			id = a[0]
		}
		var g goals.Goal
		if err := o.Client.Call(c.Context(), "ExpediteGoal", []any{MutationRoom(roomRef), id, *replace}, &g); err != nil {
			return err
		}
		if asJSON {
			return printGoal(c, g)
		}
		if g.ID == "" {
			_, err := fmt.Fprintln(c.OutOrStdout(), "no expedited goal")
			return err
		}
		state := "no longer expedited"
		if g.ExpediteUntil != nil {
			state = strings.TrimPrefix(expediteNote(g), " · ")
		}
		_, err := fmt.Fprintln(c.OutOrStdout(), g.ID, state)
		return err
	}
	note := &cobra.Command{Use: "note <id> <text>", Short: "Append a progress note to a goal's journal", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		e, err := Note(c.Context(), o, roomRef, a[0], a[1])
		if err != nil {
			return err
		}
		asJSON, err := c.Flags().GetBool("json")
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(e)
		}
		_, err = fmt.Fprintln(c.OutOrStdout(), e.ID, e.Event)
		return err
	}}
	item := &cobra.Command{Use: "item", Short: "Add and change a goal's checklist items"}
	itemAddFor := ""
	itemAdd := &cobra.Command{Use: "add <goal> <text>", Short: "Append a checklist item to a goal", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		var it goals.Item
		err := o.Client.Call(c.Context(), "AddGoalItem", []any{MutationRoom(roomRef), a[0], a[1], itemAddFor}, &it)
		if err != nil {
			return err
		}
		return printItem(c, it)
	}}
	itemAdd.Flags().StringVar(&itemAddFor, "for", "", "assignee selector, such as @name")
	itemState := func(use, short, state string) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
			var it goals.Item
			err := o.Client.Call(c.Context(), "SetGoalItemState", []any{MutationRoom(roomRef), a[0], a[1], state}, &it)
			if err != nil {
				return err
			}
			return printItem(c, it)
		}}
	}
	itemEditFor := ""
	itemEdit := &cobra.Command{Use: "edit <goal> <item> [text]", Short: "Edit an item's text or assignee", Args: cobra.RangeArgs(2, 3), RunE: func(c *cobra.Command, a []string) error {
		text := ""
		if len(a) == 3 {
			text = a[2]
		}
		var assignee *string
		if c.Flags().Changed("for") {
			assignee = &itemEditFor
		}
		var it goals.Item
		err := o.Client.Call(c.Context(), "EditGoalItem", []any{MutationRoom(roomRef), a[0], a[1], text, assignee}, &it)
		if err != nil {
			return err
		}
		return printItem(c, it)
	}}
	itemEdit.Flags().StringVar(&itemEditFor, "for", "", "assignee selector, or none to clear")
	var itemMoveTo string
	itemMove := &cobra.Command{Use: "move <goal> <item> --to <goal|new>", Short: "Move an open item to an open goal, or promote it to a standalone goal", Long: "Move one open item between open goals in this room, preserving its text and assignee. Use --to new to create a standalone goal held by the assignee, or by you if unassigned. Matching spawn instructions and completion commands follow the work. Session hierarchy stays the same.", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		var moved goals.ItemMove
		if err := o.Client.Call(c.Context(), "MoveGoalItem", []any{MutationRoom(roomRef), a[0], a[1], itemMoveTo}, &moved); err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(moved)
		}
		_, err := fmt.Fprintf(c.OutOrStdout(), "moved %s to %s\n", moved.From, moved.To)
		return err
	}}
	itemMove.Flags().StringVar(&itemMoveTo, "to", "", "destination goal ID, or new to promote the item (required)")
	_ = itemMove.MarkFlagRequired("to")
	item.AddCommand(itemAdd, itemState("done <goal> <item>", "Mark an item done", goals.ItemDone), itemState("drop <goal> <item>", "Drop an item", goals.ItemDropped), itemState("reopen <goal> <item>", "Reopen an item", goals.ItemOpen), itemEdit, itemMove)
	root.AddCommand(list, show, edit, journalCommand(o, &roomRef), finish(false), finish(true), expedite, note, item)
	return root
}

// expediteNote is the list suffix for a goal that has room priority.
func expediteNote(g goals.Goal) string {
	if g.ExpediteUntil == nil {
		return ""
	}
	return " · expedited until " + g.ExpediteUntil.Local().Format("Jan 2 15:04")
}

func printItem(c *cobra.Command, it goals.Item) error {
	asJSON, err := c.Flags().GetBool("json")
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(c.OutOrStdout()).Encode(it)
	}
	_, err = fmt.Fprintln(c.OutOrStdout(), it.ID, it.State)
	return err
}

func printGoal(c *cobra.Command, g goals.Goal) error {
	asJSON, err := c.Flags().GetBool("json")
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(c.OutOrStdout()).Encode(g)
	}
	_, err = fmt.Fprintln(c.OutOrStdout(), g.ID, g.State)
	return err
}

// isGoalCommandWord keeps likely command attempts out of goal creation.
func isGoalCommandWord(root *cobra.Command, text string) bool {
	words := strings.Fields(text)
	if len(words) != 1 {
		return false
	}
	word := strings.ToLower(words[0])
	switch word {
	case "list", "ls", "show", "done", "drop", "edit", "help", "status", "watch":
		return true
	}
	for _, cmd := range root.Commands() {
		if word == cmd.Name() || cmd.HasAlias(word) {
			return true
		}
	}
	return false
}

// Caller resolves the room and caller shared by goal and rules commands.
func Caller(s *goalsvc.Service, roomRef string) (*goalsvc.Service, room.Record, string, error) {
	ref := roomRef
	by := "owner"
	if cur, ok := mode.CurrentSession(); ok {
		info, err := s.Notify.Sessions.Store.Info(cur.ID)
		if err != nil {
			return s, room.Record{}, "", err
		}
		by = info.ID
		if ref == "" {
			ref = info.RoomID
		}
	}
	if ref == "" {
		if stamp, ok := mode.StampFromEnv(); ok {
			ref = stamp.RoomID
			if ref == "" {
				ref = stamp.Room
			}
		}
	}
	var rec room.Record
	var err error
	if ref == "" {
		rec, err = s.Notify.Rooms.CurrentRepoRoom()
	} else {
		rec, err = s.Notify.Rooms.Resolve(ref)
	}
	if err == nil && by != "owner" {
		info, _ := s.Notify.Sessions.Store.Info(by)
		if info.RoomID != rec.ID {
			err = fmt.Errorf("use the current session's room")
		}
	}
	return s, rec, by, err
}

// MutationRoom selects a room hint. The engine checks it against the caller.
func MutationRoom(ref string) string {
	if ref != "" {
		return ref
	}
	if stamp, ok := mode.StampFromEnv(); ok {
		if stamp.RoomID != "" {
			return stamp.RoomID
		}
		if stamp.Room != "" {
			return stamp.Room
		}
	}
	if rec, err := room.DefaultStore().CurrentRepoRoom(); err == nil {
		return rec.ID
	}
	return ""
}
