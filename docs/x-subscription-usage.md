# The X Subscription usage

The X Subscription gives each paid seat one weekly included pool. Team seats
do not share weekly usage. Dyna runs Grok Build against that pool.

Usage shows one dashboard card for the pool. The card links to
<https://grok.com/dashboard>. It does not claim that a Dyna account has an X
Subscription. It does not invent a limit, reset time, workspace ID, or
remaining percentage.

Official sources, checked 2026-10-18:

- [xAI announcement](https://x.ai/news/x-subscription)
- [xAI usage dashboard instructions](https://docs.x.ai/x-subscription/manage)
- [xAI per-seat usage limits](https://docs.x.ai/x-subscription/usage-limits)

## Grok billing adapter

Dyna reads the native Grok CLI 1.0.62 billing request: `GET /v1/billing?format=credits`
on `https://cli-chat-proxy.grok.com`. It uses the captured first-party OIDC session
and the CLI's account headers. Dyna does not run the CLI, renew tokens, or write
native credentials during a usage check. Redirects are refused.

The verified response identifies a seven-day unified-billing period. Dyna shows
its reset time with usage marked **Not reported**. The live response omitted
`creditUsagePercent`, and its scale remains unverified.
HTTP 401 offers Sign in and clears old quota. Temporary provider failures preserve
the saved observation. Use Refresh in Usage to check the active private login.
