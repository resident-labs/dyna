package main

import (
	"encoding/json"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func questionCommand(get func() *socketClient) *cobra.Command {
	c := &cobra.Command{Use: "question"}
	var clientID string
	ask := &cobra.Command{Use: "ask TEXT", Short: "Ask the owner a question. It needs the ask-owner grant.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := call[contract.QuestionAskRequest, contract.QuestionAskResponse](cmd.Context(), get(), contract.MethodQuestionAsk, contract.QuestionAskRequest{Text: args[0], ClientID: clientID})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	ask.Flags().StringVar(&clientID, "client-id", "", "Retry key: the same key returns the first question")
	var room string
	list := &cobra.Command{Use: "list", Short: "List a room's waiting questions and its latest answered ones", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.QuestionListRequest, contract.QuestionListResponse](cmd.Context(), get(), contract.MethodQuestionList, contract.QuestionListRequest{Room: contract.RoomID(room)})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	list.Flags().StringVar(&room, "room", "", "Room; a session may omit it for its own")
	answer := &cobra.Command{Use: "answer ID TEXT", Short: "Answer a waiting question", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := call[contract.QuestionAnswerRequest, contract.QuestionAnswerResponse](cmd.Context(), get(), contract.MethodQuestionAnswer, contract.QuestionAnswerRequest{ID: contract.QuestionID(args[0]), Text: args[1]})
		return err
	}}
	c.AddCommand(ask, list, answer)
	return c
}
