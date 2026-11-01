package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func webSearchCommand(get func() *socketClient) *cobra.Command {
	var limit int
	c := &cobra.Command{Use: "search QUERY", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := call[contract.WebSearchRequest, contract.WebSearchResponse](cmd.Context(), get(), contract.MethodWebSearch, contract.WebSearchRequest{Query: args[0], Limit: limit})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	c.Flags().IntVar(&limit, "limit", 0, "Maximum results (default 5)")
	return c
}

func websearchKeyCommand(get func() *socketClient) *cobra.Command {
	c := &cobra.Command{Use: "websearch"}
	c.AddCommand(&cobra.Command{Use: "set-key PROVIDER", Short: "Read a search key from standard input", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
		if err != nil {
			return fmt.Errorf("could not read search key")
		}
		if len(data) > 65536 {
			return fmt.Errorf("search key is too long")
		}
		_, err = call[contract.WebsearchSetKeyRequest, contract.WebsearchSetKeyResponse](cmd.Context(), get(), contract.MethodWebsearchSetKey, contract.WebsearchSetKeyRequest{Provider: args[0], Key: strings.TrimRight(string(data), "\r\n")})
		return err
	}})
	c.AddCommand(&cobra.Command{Use: "remove-key PROVIDER", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := call[contract.WebsearchRemoveKeyRequest, contract.WebsearchRemoveKeyResponse](cmd.Context(), get(), contract.MethodWebsearchRemoveKey, contract.WebsearchRemoveKeyRequest{Provider: args[0]})
		return err
	}})
	return c
}
