# Profiles, accounts, and usage

New profile opens a guided harness, model, details, and profile flow. Claude Code
is the initial harness. Manual model IDs, native defaults, custom commands, and
the existing profile editor remain available.

Profiles and Usage consume the same account service in `internal/account/service`.
Usage lists Dyna's private harness accounts; see [private harness accounts](../accounts.md).
The wizard does not create a per-profile account binding or silently switch one.

## Model discovery

| Harness | Model source |
| --- | --- |
| Claude Code | Installed help's model aliases |
| Pi | Native `--list-models`, one probe per active provider |

Discovery runs Dyna's private copy of the harness with the active private
account's home. A harness with one provider runs one probe; Pi runs one probe
for each active provider and merges the catalogs. It never runs a CLI on PATH.
Without a private copy and an active account, or when the installed schema does
not support discovery, the catalog is empty and shows a message. Enter an exact
model ID or use the harness default then.

Context and maximum input/output limits come from the native model catalog.
Bundled metadata and published allowance comparisons do not establish account
access.

Account sign-in and switching happen on the Harnesses page, not in Usage.

Credential values, native auth files, and raw provider errors stay behind the
backend boundary. The UI receives opaque references and typed projections. Native
server startup can load the harness's own plugins and normal auth behavior.

## Local activity

Dyna activity sums versioned, normalized **session turns**, including observed
usage from failed and stopped work. Workflow usage is not included in this first
read model. Coverage details expose this boundary and incomplete metrics.

Input already includes cached input; total tokens are input plus output. Cache
share uses only input with cache coverage. Claude Code session cost becomes a delta against the
saved total. Missing baselines, counter resets, and reporting gaps remain
partial or unknown. Historical unverified sums
stay stored but are excluded from normalized activity totals.

Local activity never reduces provider account quota or estimates a subscription
balance. Provider quota can include work from applications outside Dyna.

## Validation

Model parsers use sanitized fixtures and fake native commands. Service tests
cover private-account rows, cached observations and exact catalog enrichment.
Tests never read or switch the developer's actual accounts.

`web/tests/usage.html` and `tests/profiles.html` are separate browser
fixtures. They are not imported by the production application. Profile fixture
queries `?screen=test` and `?screen=save-error` run browser interaction checks;
their `body.dataset.smoke` result is `passed` or `failed`. Production components use the native
account and model APIs and never fall back to illustrative quota data.

Run desktop Go tests with `go test -tags nogate ./...` from `desktop`. Production
Linux builds use `desktop/build.sh`, which includes the WebKit gate.
