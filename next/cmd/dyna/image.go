package main

import (
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func describeImage(image contract.RoomImage) string {
	return strings.Join([]string{image.Name, "room " + string(image.Room), image.ID[:12], fmt.Sprintf("%d files", image.Files), fmt.Sprintf("%d KiB", image.Bytes>>10)}, "\t")
}

func imageCommands(get func() *socketClient) *cobra.Command {
	image := &cobra.Command{Use: "image", Short: "Room images: the toolchains of a room's boxes, and its CI runner image (ci-runner)"}
	var room string

	save := &cobra.Command{Use: "save NAME PATH", Short: "Save the folder at PATH (a Containerfile and its context) as the room's image NAME. A box takes it at its next start.", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		archive, err := skills.PackFolder(args[1], "")
		if err != nil {
			return err
		}
		result, err := call[contract.ImageSaveRequest, contract.ImageSaveResponse](cmd.Context(), get(), contract.MethodImageSave, contract.ImageSaveRequest{Name: args[0], Archive: archive, Room: contract.RoomID(room)})
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "saved", describeImage(result.Image))
		}
		return err
	}}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.ImageListRequest, contract.ImageListResponse](cmd.Context(), get(), contract.MethodImageList, contract.ImageListRequest{Room: contract.RoomID(room)})
		for _, item := range result.Images {
			fmt.Fprintln(cmd.OutOrStdout(), describeImage(item))
		}
		return err
	}}
	show := &cobra.Command{Use: "show NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := call[contract.ImageShowRequest, contract.ImageShowResponse](cmd.Context(), get(), contract.MethodImageShow, contract.ImageShowRequest{Name: args[0], Room: contract.RoomID(room)})
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), describeImage(result.Image))
			fmt.Fprintln(cmd.OutOrStdout(), "files:", strings.Join(result.Files, " "))
			fmt.Fprintln(cmd.OutOrStdout(), result.Containerfile)
		}
		return err
	}}
	remove := &cobra.Command{Use: "remove NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := call[contract.ImageRemoveRequest, contract.ImageRemoveResponse](cmd.Context(), get(), contract.MethodImageRemove, contract.ImageRemoveRequest{Name: args[0], Room: contract.RoomID(room)})
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "removed", args[0])
		}
		return err
	}}
	for _, cmd := range []*cobra.Command{save, list, show, remove} {
		cmd.Flags().StringVar(&room, "room", "", "the room, which a box already knows")
		image.AddCommand(cmd)
	}
	return image
}
