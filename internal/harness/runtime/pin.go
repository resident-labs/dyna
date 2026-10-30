package runtime

// Bundle identifies an exact installed executable. Launchers must set
// DISABLE_UPDATES=1 for Claude Code.
type Bundle struct {
	Harness    string `json:"harness"`
	Version    string `json:"version"`
	Executable string `json:"executable"`
	Directory  string `json:"directory"`
}

type Pin struct {
	Bundle     Bundle `json:"bundle"`
	AccountID  string `json:"accountId"` // observed active account; never binds resume
	Generation string `json:"generation"`
}
