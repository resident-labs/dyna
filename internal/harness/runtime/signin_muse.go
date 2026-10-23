package runtime

import "regexp"

// museDeviceURL accepts only Meta's device page and its one-time code.
var museDeviceURL = regexp.MustCompile(`^https://auth\.meta\.com/oauth/device/\?code=([A-Z0-9]{4}-[A-Z0-9]{4})$`)

func museDeviceArgv(argv []string) []string {
	return append([]string(nil), argv...)
}

func parseMuseSignIn(st *SignInState, line string) {
	match := museDeviceURL.FindStringSubmatch(line)
	if match == nil {
		return
	}
	st.URL = line
	st.Code = match[1]
	st.Phase = signInWaiting
}
