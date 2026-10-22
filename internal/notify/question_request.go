package notify

// AskRequest asks the engine to store and deliver a question. The engine
// verifies Claim against its caller, sharing the post identity rules.
type AskRequest struct {
	Room      string
	To        string
	Text      string
	Meanwhile string
	Options   []string
	Files     []File
	Claim     Claim
}
