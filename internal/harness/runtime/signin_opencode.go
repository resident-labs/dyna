package runtime

import (
	"regexp"
	"strings"
)

var (
	// OpenCode includes the CLI client ID in its own device URL.
	openCodeDeviceURL  = regexp.MustCompile(`^https://opencode\.ai/console/device\?user_code=([A-Z0-9]{4}-[A-Z0-9]{4})(?:&client_id=opencode-cli)?$`)
	openCodeDeviceCode = regexp.MustCompile(`^Enter code: ([A-Z0-9]{4}-[A-Z0-9]{4})$`)
)

// OpenCode Go supports only key entry, which still requires its native terminal.
func openCodeKeySignIn(account NativeAccount) bool {
	return account.Harness == "opencode" && strings.HasPrefix(account.Identity, "opencode:opencode-go:")
}

func openCodeDeviceArgv(argv []string) []string {
	argv = append([]string(nil), argv...)
	// The account manager leaves the target unset. Choose OpenCode Zen,
	// which supports device sign-in without a terminal.
	if len(argv) == 3 {
		argv = append(argv, "opencode")
	}
	return append(argv, "--method", "device")
}

func parseOpenCodeSignIn(st *SignInState, line string) {
	// OpenCode's CLI adds a bullet even when its output is a pipe.
	line = strings.TrimPrefix(line, "● ")
	if match := openCodeDeviceURL.FindStringSubmatch(line); match != nil {
		st.URL = line
		st.Code = match[1]
	} else if match := openCodeDeviceCode.FindStringSubmatch(line); match != nil {
		st.Code = match[1]
	} else {
		return
	}
	if st.URL != "" && st.Code != "" {
		st.Phase = signInWaiting
	}
}
