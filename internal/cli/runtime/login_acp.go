package runtime

import (
	"errors"
	"os"
	"strings"

	"dyna-agent/internal/account/native"
	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"github.com/spf13/cobra"
)

// antigravityACPLogin runs the ACP server's own Google sign-in in the private
// home that BeginLogin prepared. The server sits beside agy, and one shared
// assembly (AntigravityACPEnv) builds its environment.
func antigravityACPLogin(c *cobra.Command, agy, expect string, flow *harnessruntime.NativeLogin) error {
	// The ACP server's GEMINI_HOME is the paired private HOME, never the native scope directory.
	home := envValue(flow.Env, "HOME")
	if home == "" || home != harnesshome.Home(flow.Cwd) {
		return errors.New("the private account home does not match the native scope")
	}
	server := harnessruntime.AntigravityACPExecutable(agy)
	if _, err := os.Stat(server); err != nil {
		return errors.New("the Antigravity ACP server is not installed beside agy")
	}
	// The session adapter overlays the same map, so login and worker launches
	// run the server with one environment assembly.
	env := append(os.Environ(), flow.Env...)
	for key, value := range harnessruntime.AntigravityACPEnv(agy, home) {
		env = append(env, key+"="+value)
	}
	login := native.AntigravityACPLogin{
		Server: server,
		Env:    env,
		Home:   home,
		Out:    c.OutOrStdout(),
		Expect: expect,
	}
	return login.Run(c.Context())
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}
