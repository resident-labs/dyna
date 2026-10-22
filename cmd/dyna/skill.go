package main

import (
	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/harnessconfig"
	"dyna-agent/internal/cli/setup"
)

func skillCmd() *cobra.Command {
	c := setup.NewSkillCommand()
	harnessconfig.AddCatalogList(c, "skill")
	harnessconfig.AddWriteCommands(c, "skill")
	return c
}

func mcpCmd() *cobra.Command {
	c := harnessconfig.NewMCPCommand()
	harnessconfig.AddWriteCommands(c, "mcp")
	return c
}
