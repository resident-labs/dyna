// Package export exposes tested local integration exports.
package export

import (
	clisync "dyna-agent/internal/cli/sync"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	return clisync.OperationCommand("export", "GitExport", "Test integration against origin/main and open or update its export PR")
}
