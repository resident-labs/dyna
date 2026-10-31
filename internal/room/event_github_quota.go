package room

// GitHubQuotaEvent reports an observed primary quota window without exposing
// the account. The post ID distinguishes accounts without putting them on the card.
type GitHubQuotaEvent struct {
	Host      string `json:"host"`
	Resource  string `json:"resource"`
	Remaining int    `json:"remaining,omitempty"`
	Limit     int    `json:"limit"`
	Reset     string `json:"reset"`
}

func (GitHubQuotaEvent) eventFamily() EventFamily { return FamilyGitHubQuota }
func (GitHubQuotaEvent) eventType() EventType     { return TypeQuotaLow }
func (e GitHubQuotaEvent) eventSubject() string   { return e.Host + "/" + e.Resource }
func (e GitHubQuotaEvent) eventDedupe() string    { return e.Reset }
