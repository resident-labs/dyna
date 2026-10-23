package runtime

import "regexp"

// grokDeviceURL accepts only xAI's device page and its one-time code.
var grokDeviceURL = regexp.MustCompile(`^https://accounts\.x\.ai/oauth2/device\?user_code=([A-Z0-9]{4}-[A-Z0-9]{4})$`)

func grokDeviceArgv(argv []string) []string {
	return append(append([]string(nil), argv...), "--device-auth")
}

// Grok prints the code in the URL, without a separate code line.
func parseGrokSignIn(st *SignInState, line string) {
	match := grokDeviceURL.FindStringSubmatch(line)
	if match == nil {
		return
	}
	st.URL = line
	st.Code = match[1]
	st.Phase = signInWaiting
}
