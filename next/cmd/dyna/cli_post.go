package main

import (
	"context"
	"fmt"
	"os"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	"github.com/spf13/cobra"
)

func postCommand(getClient func() client) *cobra.Command {
	var postRoom, htmlPath, update string
	var allowNet bool
	var forward string
	var mentions, paths []string
	postCmd := &cobra.Command{Use: "post TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		upload := func(ctx context.Context, path string) (contract.Attachment, error) {
			return c.Upload(ctx, contract.RoomID(postRoom), path)
		}
		request, checks, err := (post.Preparer{Upload: upload, Render: visual.Preview}).Prepare(cmd.Context(), tools.PostInput{Room: contract.RoomID(postRoom), Text: args[0], Mentions: mentions, Paths: paths, HTML: htmlPath, AllowNet: allowNet, VisualUpdate: contract.EventID(update), Forward: contract.EventID(forward)})
		if err != nil {
			return err
		}
		printVisualChecks(cmd.ErrOrStderr(), checks)
		request.ToolCallID = os.Getenv(contract.ToolCallIDEnv)
		r, err := c.Post(cmd.Context(), request)
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Event)
		}
		return err
	}}
	postCmd.Flags().StringVar(&htmlPath, "html", "", "Attach an HTML visual card")
	postCmd.Flags().StringVar(&update, "update", "", "Add a version to a visual post")
	postCmd.Flags().BoolVar(&allowNet, "allow-net", false, "Allow HTTPS resources in the visual")
	postCmd.Flags().StringArrayVar(&paths, "attach", nil, "Attach a file (repeatable)")
	postCmd.Flags().StringVar(&forward, "forward", "", "Forward a post by ID, with TEXT as your note (may be empty)")
	postCmd.Flags().StringVar(&postRoom, "room", "", "Room ID")
	postCmd.Flags().StringSliceVar(&mentions, "mention", nil, "Session mentions")
	return postCmd
}
