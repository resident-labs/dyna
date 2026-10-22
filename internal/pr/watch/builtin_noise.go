package watch

import "strings"

// These full templates came from harvested bot comments. Unlike a keyword
// or prefix rule, normalized whole-template equality keeps appended findings.
// Uncertain summaries, confidence reports and answers stay kept.
var builtinNoise = []struct{ id, bot, text string }{
	{"codex-security-quota", "chatgpt-codex-connector[bot]", "You have reached your Codex usage limits for security reviews. Please try again later."},
	{"codex-code-quota", "chatgpt-codex-connector[bot]", "You have reached your Codex usage limits for code reviews. You can see your limits in the [Codex usage dashboard](https://chatgpt.com/codex/cloud/settings/usage). To continue using code reviews, add credits to your account and enable them for code reviews in your [settings](https://chatgpt.com/codex/cloud/settings/code-review)."},
	{"codex-payer-quota", "chatgpt-codex-connector[bot]", "The account paying for this security review has reached its Codex usage limits. The payer can check the [Codex usage dashboard](https://chatgpt.com/codex/cloud/settings/usage). For personal accounts, using credits requires enabling “Use credits for security reviews” in [Code review settings](https://chatgpt.com/codex/cloud/settings/code-review). If you do not manage the paying account, contact this repository's admins."},
	{"sourcery-budget", "sourcery-ai[bot]", "Sorry @leynos, you've used your own review budget of 250,000 diff characters for the last 7 days. You can request another review in 2 days and 21 hours by commenting `@sourcery-ai review`. [Upgrade](https://app.sourcery.ai/login?connection=github&git_namespace_id=746767&from_surface=github_bot&intent=rate_limit_hit) to get a review now."},
}

func builtinNoiseDecision(e Event) (Decision, bool) {
	if !e.IsBot || protectedFeedback(e) {
		return Decision{}, false
	}
	template := ""
	for _, rule := range builtinNoise {
		if !strings.EqualFold(rule.bot, e.Commenter) {
			continue
		}
		if template == "" {
			template = normalizeNoise(e.Text)
		}
		if template == normalizeNoise(rule.text) {
			return Decision{Action: "drop", Reason: "builtin:" + rule.id}, true
		}
	}
	return Decision{}, false
}
