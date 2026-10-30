package store

type Plan struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Window struct {
	Reason      WindowReason `json:"reason,omitempty"`
	Period      string       `json:"period,omitempty"`
	PeriodStart string       `json:"periodStart,omitempty"`
	ID          string       `json:"id"`
	Label       string       `json:"label"`
	UsedPercent *float64     `json:"usedPercent,omitempty"`
	Used        *float64     `json:"used,omitempty"`
	Limit       *float64     `json:"limit,omitempty"`
	// Balance is the remaining money when a provider reports a balance
	// instead of a quota window. Unit names its currency.
	Balance  *float64 `json:"balance,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	ResetsAt string   `json:"resetsAt,omitempty"`
}

// WindowReason is the classified reason a window carries no usage reading. It
// is typed so the CLI and the web app show designed copy instead of the reader's
// sentence, because reading the sentence to decide presentation made reader
// wording a hidden protocol.
type WindowReason string

const (
	// WindowReasonNotReported means the reader found the window but no allowance
	// figure for it.
	WindowReasonNotReported WindowReason = "not-reported"
	// WindowReasonProbableReset means the window's reset time has passed since
	// the reading, so its figure stays unknown until the next read.
	WindowReasonProbableReset WindowReason = "probable-reset"
)

// UsageReason is the classified reason an account has no current usage reading.
// It is typed so the CLI and the web app can show a designed message instead of
// raw reader text.
type UsageReason string

const (
	// UsageReasonNotReadable means the provider does not publish usage this
	// credential can read, so a retry will not help.
	UsageReasonNotReadable UsageReason = "not-readable"
	// UsageReasonExpired means the saved sign-in can no longer read usage. The
	// owner signs in again.
	UsageReasonExpired UsageReason = "expired"
	// UsageReasonSecret means a reader copied the account's credential, or a
	// value that looks like one, into its result. Dyna rejects that result.
	UsageReasonSecret UsageReason = "secret"
	// UsageReasonProviderError means a temporary provider or network failure.
	UsageReasonProviderError UsageReason = "provider-error"
	// UsageReasonRateLimited means the provider asked Dyna to wait.
	UsageReasonRateLimited UsageReason = "rate-limited"
	// UsageReasonNoWindows means the provider answered but reported no windows.
	UsageReasonNoWindows UsageReason = "no-windows"
)

// Provider is the sign-in service an account belongs to. A harness consumes an
// account; it does not own it.
type Provider string

const (
	ProviderAnthropic  Provider = "anthropic"
	ProviderChatGPT    Provider = "chatgpt"
	ProviderKimiCode   Provider = "kimi-code"
	ProviderXAI        Provider = "xai"
	ProviderMetaMuse   Provider = "meta-muse"
	ProviderOpenCodeGo Provider = "opencode-go"
	ProviderDeepSeek   Provider = "deepseek"
	ProviderZAI        Provider = "zai"
	// ProviderBrave is the Brave Search API key the web search tool uses when
	// the providers above cannot answer.
	ProviderBrave Provider = "brave"
	// ProviderExa is the Exa Search API key the web search tool uses last.
	ProviderExa Provider = "exa"
	// ProviderTavily is the Tavily Search API key the web search tool uses last.
	ProviderTavily Provider = "tavily"
	// ProviderMCPBearer is a bearer credential scoped to one room MCP server.
	ProviderMCPBearer Provider = "mcp-bearer"
)

// Kind is how an account signed in: an interactive sign-in or a pasted key.
type Kind string

const (
	KindOAuth  Kind = "oauth"
	KindAPIKey Kind = "api_key"
)
