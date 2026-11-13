package main

import (
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// goalCommand holds the goal commands. Inside a box --room may stay empty: the engine uses the caller's room.
func goalCommand(getClient func() client) *cobra.Command {
	goal := &cobra.Command{Use: "goal", Short: "Work on goals and their checklists"}
	var roomID string
	goal.PersistentFlags().StringVar(&roomID, "room", "", "Room ID")
	printJSON := func(cmd *cobra.Command, value any) error {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	var id, holder, text, doneWhen, clientID string
	set := &cobra.Command{Use: "set", Short: "Create a goal, change one, or hand it to another session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().GoalSet(cmd.Context(), contract.GoalSetRequest{ClientID: clientID, Room: contract.RoomID(roomID), Goal: contract.GoalID(id), Session: contract.SessionID(holder), Text: text, DoneWhen: doneWhen})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Goal)
		}
		return err
	}}
	set.Flags().StringVar(&id, "goal", "", "Goal to change")
	set.Flags().StringVar(&holder, "session", "", "Session that holds it")
	set.Flags().StringVar(&text, "text", "", "What the goal is")
	set.Flags().StringVar(&doneWhen, "done-when", "", "When it is done")
	set.Flags().StringVar(&clientID, "client-id", "", "Retry key")
	var all bool
	var listHolder string
	list := &cobra.Command{Use: "list", Short: "List a room's goals with their items", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().GoalList(cmd.Context(), contract.GoalListRequest{Room: contract.RoomID(roomID), Session: contract.SessionID(listHolder), All: all})
		if err != nil {
			return err
		}
		return printJSON(cmd, r.Goals)
	}}
	list.Flags().StringVar(&listHolder, "session", "", "Only this session's goals")
	list.Flags().BoolVar(&all, "all", false, "Include ended goals")
	var item, parent int
	var itemText, itemState string
	itemCmd := &cobra.Command{Use: "item GOAL", Short: "Add a todo item, a subtask with --parent, or change one with --item", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().GoalItem(cmd.Context(), contract.GoalItemRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0]), Item: item, Parent: parent, Text: itemText, State: contract.GoalItemState(itemState)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Item)
		}
		return err
	}}
	itemCmd.Flags().IntVar(&item, "item", 0, "Item number to change")
	itemCmd.Flags().IntVar(&parent, "parent", 0, "Item number a new item is a subtask of")
	itemCmd.Flags().StringVar(&itemText, "text", "", "Item text")
	itemCmd.Flags().StringVar(&itemState, "state", "", "open, done or dropped")
	var drop bool
	done := &cobra.Command{Use: "done GOAL SUMMARY", Short: "End a goal", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().GoalDone(cmd.Context(), contract.GoalDoneRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0]), Summary: args[1], Drop: drop})
		return err
	}}
	done.Flags().BoolVar(&drop, "drop", false, "Give the goal up instead")
	journal := &cobra.Command{Use: "journal GOAL [TEXT]", Short: "Add to a goal's journal and read its newest 5 lines", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.GoalJournalRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0])}
		if len(args) == 2 {
			req.Text = args[1]
		}
		r, err := getClient().GoalJournal(cmd.Context(), req)
		if err != nil {
			return err
		}
		return printJSON(cmd, r)
	}}
	goal.AddCommand(set, list, itemCmd, done, journal)
	return goal
}
