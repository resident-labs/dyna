package runtime

import (
	harnessruntime "dyna-agent/internal/harness/runtime"
	"github.com/spf13/cobra"
)

// antigravityACPLogin runs the ACP server's own Google sign-in in the private
// home that BeginLogin prepared. The sign-in lives in harnessruntime, so the
// Settings Sign in button runs the same code in the same home.
func antigravityACPLogin(c *cobra.Command, agy, expect string, flow *harnessruntime.NativeLogin) error {
	return harnessruntime.AntigravityACPSignIn(c.Context(), agy, flow.Cwd, flow.Env, expect, c.OutOrStdout())
}
