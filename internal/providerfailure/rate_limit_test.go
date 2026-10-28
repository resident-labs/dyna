package providerfailure

import "testing"

// A per-minute 429 waits and retries on the same account; a 429 that says the
// quota is spent still moves the account.
func TestRateLimitSplitsShortFromSpent(t *testing.T) {
	cases := []struct {
		detail string
		want   Reason
	}{
		{"429 Rate limit reached for requests per minute. Please try again in 6s.", Capacity},
		{"{\"code\":\"1302\",\"message\":\"Rate limit reached for requests\"}", Capacity},
		{"{\"code\":\"1299\",\"message\":\"Rate limit reached for requests\"}", Quota},
		{"{\"code\":\"429\",\"message\":\"Rate limit reached for requests per minute. Please try again in 6s.\"}", Capacity},
		{"429 too many requests, retry-after: 20", Capacity},
		{"429 You exceeded your current quota, please try again in 3 seconds", Quota},
		{"429 usage limit reached", Quota},
		{"429 too many requests", Quota},
	}
	for _, c := range cases {
		if got, ok := Classify(c.detail); !ok || got != c.want {
			t.Errorf("Classify(%q) = %q, %v; want %q", c.detail, got, ok, c.want)
		}
	}
	if got, _ := ClassifyStderr("error 429: tokens per min limit, try again in 2s"); got != Capacity {
		t.Errorf("stderr per-minute limit = %q, want capacity", got)
	}
	if got, _ := ClassifyStderr("error 429: quota exceeded, try again in 2s"); got != Quota {
		t.Errorf("stderr spent quota = %q, want quota", got)
	}
}
