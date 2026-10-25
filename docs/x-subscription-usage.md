# The X Subscription usage

The X Subscription gives each paid seat one weekly included pool. Team seats
do not share weekly usage. Dyna keeps the saved Grok account to read that pool.

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

The response names its period as `USAGE_PERIOD_TYPE_WEEKLY` or
`USAGE_PERIOD_TYPE_MONTHLY`. Dyna shows that as a Weekly or Monthly window:
`creditUsagePercent` is the used share, and `currentPeriod.end` is the reset.
Grok omits a zero percentage while keeping the full period, so a missing
percentage on a valid weekly or monthly period is 0, as T3 Code reads it. A
period that is missing, unparseable or ends before it starts is an unusable
response.
HTTP 401 offers Sign in and clears old quota. Temporary provider failures preserve
the saved observation. Use Refresh in Usage to check the active private login.
