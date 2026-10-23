package runtime

import (
	"net/url"
	"strings"
)

// parseClaudeCodeSignIn reads the authorization link printed by
// claude auth login --claudeai. The CLI accepts the callback code over stdin.
func parseClaudeCodeSignIn(st *SignInState, line string) {
	for _, field := range strings.Fields(line) {
		u, err := url.Parse(field)
		if err != nil || u.Scheme != "https" || u.Host != "claude.com" || u.User != nil || u.Path != "/cai/oauth/authorize" {
			continue
		}
		st.URL = field
		st.Phase = signInWaiting
		st.Message = "Open the link, sign in, then paste the code from the callback page."
		return
	}
}
