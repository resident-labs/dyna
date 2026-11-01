package executor

// DynaCodexUserAgent lets the management usage reader send the client identity
// the Codex executor already sends, which avoids Cloudflare client blocks.
func DynaCodexUserAgent() string { return codexUserAgent }
