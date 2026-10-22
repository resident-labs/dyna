package setup

import (
	"fmt"

	"github.com/spf13/cobra"

	"dyna-agent/internal/guidance"
)

// The skill only points at `dyna guide`, the one source of Dyna guidance.
var skillBody = guidance.Body

const skillFrontmatter = guidance.Frontmatter

// NewSkillCommand prints the orchestrator skill for agents outside Dyna. Owners
// who want it copy it into their own harness by hand; Dyna does not write there.
func NewSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage Dyna's skill catalog",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the Dyna orchestrator skill for manual installation outside Dyna",
		Run:   func(c *cobra.Command, _ []string) { fmt.Fprint(c.OutOrStdout(), skillFrontmatter+skillBody) },
	})
	return cmd
}
