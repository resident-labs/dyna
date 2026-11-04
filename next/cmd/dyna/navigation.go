package main

import (
	"encoding/json"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// navigationCommands are the CLI of the navigation tools: search, history and transcript (7.9).
func navigationCommands(get func() *socketClient) []*cobra.Command {
	search := &cobra.Command{Use: "search", Short: "Search posts, sessions and rooms across rooms; DMs only in your own room"}
	var req contract.SearchRequest
	for _, kind := range []struct {
		name   string
		method contract.Method
		short  string
	}{
		{"posts", contract.MethodSearchPosts, "Posts that have every word of QUERY, newest first"},
		{"sessions", contract.MethodSearchSessions, "Sessions whose name or goal has the words, open first"},
		{"rooms", contract.MethodSearchRooms, "The rooms, narrowed by QUERY when given"},
	} {
		cmd := &cobra.Command{Use: kind.name + " [QUERY]", Short: kind.short, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			r := req
			if len(args) > 0 {
				r.Query = args[0]
			}
			result, err := call[contract.SearchRequest, contract.SearchResponse](cmd.Context(), get(), kind.method, r)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}}
		search.AddCommand(cmd)
	}
	search.PersistentFlags().StringVar(&req.Room, "room", "", "A room name or ID; here is yours; omit for all rooms")
	search.PersistentFlags().StringVar(&req.From, "from", "", "For posts: a session name, or owner")
	search.PersistentFlags().StringVar(&req.After, "after", "", "A result's next")
	search.PersistentFlags().IntVar(&req.Limit, "limit", 0, "At most 25")

	var history contract.HistoryReadRequest
	var around, before, after, post string
	historyCmd := &cobra.Command{Use: "history", Short: "Read one conversation in time order: any room's feed, or a DM in your own room", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		history.Around, history.Before, history.After, history.Post = contract.EventID(around), contract.EventID(before), contract.EventID(after), contract.EventID(post)
		result, err := call[contract.HistoryReadRequest, contract.HistoryReadResponse](cmd.Context(), get(), contract.MethodHistoryRead, history)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	historyCmd.Flags().StringVar(&history.Room, "room", "", "A room name or ID; omit for yours")
	historyCmd.Flags().StringVar(&history.DM, "dm", "", "A session name or ID: the owner's DM with it, in your own room")
	historyCmd.Flags().StringVar(&around, "around", "", "Centre the page on this post ID")
	historyCmd.Flags().StringVar(&before, "before", "", "Older posts than this post ID")
	historyCmd.Flags().StringVar(&after, "after", "", "Newer posts than this post ID")
	historyCmd.Flags().StringVar(&post, "post", "", "Read this post whole")
	historyCmd.Flags().IntVar(&history.Limit, "limit", 0, "At most 50")

	var transcript contract.TranscriptDigestRequest
	var turn, turnBefore string
	transcriptCmd := &cobra.Command{Use: "transcript SESSION", Short: "Read a session's turns, in your own room", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		transcript.Session, transcript.Turn, transcript.Before = args[0], contract.TurnID(turn), contract.TurnID(turnBefore)
		result, err := call[contract.TranscriptDigestRequest, contract.TranscriptDigestResponse](cmd.Context(), get(), contract.MethodTranscriptDigest, transcript)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	transcriptCmd.Flags().StringVar(&turn, "turn", "", "Read one turn")
	transcriptCmd.Flags().StringVar(&turnBefore, "before", "", "Older turns than this one")
	transcriptCmd.Flags().StringVar(&transcript.Item, "item", "", "Read one cut item whole (needs --turn)")
	transcriptCmd.Flags().IntVar(&transcript.Offset, "offset", 0, "Where in the item to read from")
	transcriptCmd.Flags().IntVar(&transcript.Limit, "limit", 0, "At most 5")
	return []*cobra.Command{search, historyCmd, transcriptCmd}
}
