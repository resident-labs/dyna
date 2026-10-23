package callspec

// Legacy is one call name that the engine keeps serving after its replacement
// arrived. A session can outlive an engine self-update, so the CLI it still
// runs must be able to finish its work against the newer engine.
type Legacy struct {
	// Replacement is the call that new clients use instead.
	Replacement string
	// Why says what renamed the call and what breaks without this entry.
	Why string
}

// LegacyCalls are the call names that only older clients still send. Each one
// needs a shim in the engine that records through the replacement, and each
// stays for one release after the build that added the replacement.
// TestClientCallsAreServedByTheEngine checks that every name here is served.
//
// TODO: legacy/shim remove later - drop "RecordPRVerdict" one release after the
// build that introduced RecordReviewResult, together with its shim in
// internal/pr/view/verdict.go.
var LegacyCalls = map[string]Legacy{
	"RecordPRVerdict": {
		Replacement: "RecordReviewResult",
		Why:         "PR #345 replaced the text verdict with typed review results; a reviewer running the previous dyna could not record its verdict after the engine self-updated.",
	},
}
