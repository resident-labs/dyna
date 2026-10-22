package subagent

import (
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
	"strconv"
)

func roomResumeCommand(newManager func() *subagent.Manager) *cobra.Command {
	return &cobra.Command{Use: "_resume-room <room> <revision>", Hidden: true, Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, args []string) error {
		revision, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return err
		}
		return newManager().ProcessRoomResume(c.Context(), args[0], revision)
	}}
}
