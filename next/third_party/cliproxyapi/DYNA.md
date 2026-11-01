# Dyna's CLIProxyAPI vendor

Dyna vendors this source for the v2 engine. The upstream repository is `https://github.com/router-for-me/CLIProxyAPI`. This subtree started at tag `v8.0.48` and now sits at tag `v8.0.49`, commit `250d598175db61a4e99ac38b9164ed04745754e5`. The module path is `github.com/router-for-me/CLIProxyAPI/v8`. The upstream MIT license and all upstream notices remain unchanged.

## Dyna changes

P0 adds this documentation and changes no upstream CLIProxyAPI file. It changes no proxy behavior. The folder has its own `go.mod`, so the v1 root module and `go test ./...` skip it.

The v1 gofmt check, `scripts/ci-gofmt.sh`, skips this folder. One upstream test file does not pass gofmt, and Dyna keeps upstream formatting unchanged. This is a change to Dyna's script, not to the vendored source.

Each later patch to this folder must be its own commit. Each patch must add an entry here that names its reason and the Dyna code it serves.

- The SDK builder accepts an already-bound listener. `engine/model` uses it to avoid a loopback port reservation race that could expose the client key.
- Upstream PR [#5434](https://github.com/router-for-me/CLIProxyAPI/pull/5434), head `fa29f0716067730b6aedbf3e3602ad7be489f9b7`, adds normalized Claude OAuth quota reports for `engine/model`. The patch changes upstream v7 import paths to v8; drop it when a subtree update includes the upstream change. Patch size: 812 lines added and 15 removed, including tests. The upstream body-cap test passed in 0.02 seconds on hermeslab.

- Preserve `seven_day_oauth_apps` alongside nonempty `limits` when the normalized limits omit that window. `engine/model` uses it for the correct Claude weekly quota. This separate patch adds 38 lines and removes 3, including its regression test; drop it when upstream fixes mixed responses.
- Preserve `five_hour` when nonempty `limits` omit an unscoped session window. `engine/model` needs the session quota for routing and display. The patch adds 12 product lines and removes 2, plus 27 test lines. The 2026-10-31 upstream issue and PR search found no matching patch; #5434 supplies the original endpoint. Drop this patch when upstream fixes mixed responses.
- OAuth in-memory handoff and adoption status: 124 product lines added, 4 removed. Dyna enables it for Claude and Codex callbacks, and it exposes adopted credential IDs for the supported flows. Upstream PR #4800 handles a busy callback port, not secret-free handoff; issue #295 asks for manual callbacks, not this storage boundary.
- Durable credential writes: 18 product lines added, 1 removed. An opt-in store rejects publication after a failed save with a typed, secret-free error. If a provider already invalidated the old token, the owner must sign in again. Upstream PR #6295 makes file replacement atomic but does not reject an uncommitted refresh.
- Codex metadata merge SDK bridge: 5 product lines added. The SQLite adapter reuses upstream’s `MergeCodexCredentialsFromDisk` rather than replacing token partitions on a metadata-only write. Upstream PR #3843 addresses the same preservation for file-backed stores.
- Durable API-key registration and catalog-only providers: 38 product lines added, 1 removed. `cmd/dyna` registers a stored key and its fixed model catalog without a keyless fallback. Upstream issue #6201 asks for separate credentials and configuration; the checked open PRs do not implement this registration boundary.
- Staged credential activation: 21 product lines added, 6 removed. Credentials stay disabled until Dyna binds an account. A restart enables only credentials with a bound account. The 2026-10-31 upstream search found no fitting staged activation patch; PR #6715 covers a Codex login route.
- Shared OAuth/key staging hook: 7 product lines added, 1 removed. Both creation paths use the same disabled staging boundary; the upstream search for staged OAuth activation remains the same as above.
- Non-Codex completion adoption: 18 product lines added, 5 removed. It verifies the disabled runtime grant before the completion notification. The 2026-10-31 upstream OAuth adoption search found PR #4734 for Codex recovery, not this private handoff. A build-tagged test bridge joins the real management status and Dyna RPC paths without entering production builds.
- OAuth completion notification: 11 product lines added. A private hook tells the engine when a saved OAuth session completes, so unpolled attempts cannot leave a routable orphan. The 2026-10-31 upstream searches found no fitting completion hook; PR #6715 adds a Codex login route rather than a store handoff.
- Private credential discovery and typed runtime status: 83 product lines added. The SDK exposes pathless credentials to the local adapter. `RequiresSignIn` is runtime-only, so an unverified credential stays unknown. Upstream issue #4585 confirms file-path listing gaps; #6201 remains open, and no checked upstream patch covers this adapter.

- Atomic OAuth grant replacement for existing credentials: 69 product lines added and 4 removed, plus regression tests. It uses the existing per-auth mutation gate and registration epoch to discard refreshes from an older grant, while preserving upstream partition merges. The existing request-info seam carries raw new-grant identity captured before upstream merges old metadata, so Dyna can reject a missing or different identity without skipping that merge. `engine/model` uses it for safe account reconnect. The upstream search on 2026-10-31 found issue #6288 on retry and #4945 on relogin metadata, but no matching atomic refresh-versus-login patch; upstream #4734 addresses targeted Codex recovery.

- Request-scoped credential eligibility: the engine sends encoded bound credential IDs over its private proxy socket; the handler removes the header and captures the set in context. Local selection, affinity and retries must stay within that set. The 2026-10-31 upstream search found issue [#6972](https://github.com/router-for-me/CLIProxyAPI/issues/6972) for external single-ID dispatch and PR [#5660](https://github.com/router-for-me/CLIProxyAPI/pull/5660) for provider prefixes, but neither implements a request-local multi-credential allowlist.

- OpenCode Go session forwarding adapts upstream PR [#5833](https://github.com/router-for-me/CLIProxyAPI/pull/5833), head `6adf6ea6008c8fdbc934139d743ab030d0af55d2`, for the OpenAI-compatible executor. It uses the canonical session identity on streaming and non-streaming requests, without leaking it to unrelated providers. Drop this patch when upstream includes it.

- Private fixed-upstream Kimi and z.ai search: the proxy authenticates with its request access manager and selects only active credentials in the request's encoded allowlist. It retains secrets in the proxy and returns bounded, sanitized refusal reasons. The 2026-10-31 upstream search found no matching search endpoint; issue #5236 is unrelated. Dyna uses the v1 provider protocols and disables redirects.

- Upstream PR [#6467](https://github.com/router-for-me/CLIProxyAPI/pull/6467), head `212ebf61df925158f43f8d8401e6ebc3b83be0e0`, records Meta's `response.subscription_usage` as passive `X-Meta-*` quota signals for the `auth-files/quota` Meta reader that serves `engine/model`. The PR is open and conflicts with `v8.0.49`, so this adapts it. It keeps the recording: 134 product lines added, 1 removed, plus 223 test lines. It drops the PR's forwarding of the event, because upstream's `TestMetaExecutor_FiltersSubscriptionUsageGlobally` hides the event from every client and Dyna's Pi does not read it. It drops the PR's reload hunk, because upstream's file-snapshot merge in `updateInternal` already keeps the observation. It also accepts the typeless payloads upstream's filter treats as this event. Drop this patch when upstream records the event.

- Meta device sign-in adoption: 1 product line changed, plus a 42-line test. `engine/model` binds a Meta account only after it sees an adopted credential, as for Kimi and xAI, so the Meta flow reports through the existing Dyna completion path. Meta OAuth itself is upstream; the 2026-11-01 check of `v8.0.49` found no adoption path for it.

- xAI `api-call` refresh through the manager: 17 product lines added, 128 removed, plus 5 test lines added and 2 removed. `resolveXAIToken` refreshed outside the credential's refresh lock and saved the result itself, and xAI reportedly rotates refresh tokens, so a concurrent refresh could spend the grant. It now calls `ForceRefreshAuth` behind a singleflight. This protects the `xai` credentials `engine/model` binds; Dyna's own usage reader sends the stored token and never refreshes. The 2026-11-01 check of `v8.0.49` found the old path unchanged upstream.

- Normalized usage for every provider, starting with Codex: 359 product lines added, plus 307 test lines. `engine/model` keeps its one `auth-files/quota` call per credential, and the owner decided usage reporting belongs in the proxy. Non-Claude entries add `state`, `balances`, and window `name` and `seconds`; Claude's entries and status handling do not change. `auth_files_quota.go` gains only two embedded structs and two dispatch checks, and each reader lives in its own `dyna_usage_<provider>.go`. A reader sends the stored credential, never refreshes it, follows no redirect and forwards no upstream body. It fails to `unknown`, `unreachable` or `rate_limited`, never to 0 or to an expired sign-in. Upstream refused quota in core (#7018, #5034), so this stays local and the plugin system stays off. Codex reads `wham/usage` as `openai/codex` backend-client does and classifies windows by `limit_window_seconds`. `DynaCodexUserAgent` reuses the executor's client identity. Upstream PR #4488 offers a different `/v1/account/limits` route for three providers in 1362 lines, so taking it would not reduce this code. Drop this patch if upstream ships per-provider usage.

- Kimi usage reader: 166 product lines added, plus 122 test lines. It serves `engine/model` from `/coding/v1/usages` on the credential's domain, as `kimi-code` managed-usage.ts does. Official parsing shows false zeros (kimi-code #4133, #3817, #4473), so when legacy counters and a ratio pool cover one duration and reset, the more exhausted reading wins (CodexBar #4306). Monthly pools always show, and `boosterWallet` is a display-only balance. Upstream PR #4373 adds a background API-key probe for cooldowns in 2622 lines, which does not replace this reader.

- xAI usage reader: 84 product lines added, plus 71 test lines. It serves `engine/model` from the `grok-build` billing call, with the proxy's npm-tracked client version. It reads `creditUsagePercent`, then the deprecated `monthlyLimit` and `used`. A missing percent is 0% only beside a complete weekly or monthly period, as T3 Code decided, and otherwise unknown.

- z.ai usage reader: 60 product lines added, 3 removed, plus 77 test lines. It serves `engine/model` with the raw key on a fixed host, as z.ai's plugin and cc-switch do. It classifies limits by `unit`, reads `CREDIT_LIMIT`, skips `TIME_LIMIT`, omits impossible reset times and checks the envelope, because a pay-as-you-go key answers HTTP 200 with `success` false.

- DeepSeek balance reader: 36 product lines added, 4 removed, plus 38 test lines. It serves `engine/model` and the Home balance from the documented `/user/balance`, one balance per currency. DeepSeek has no limit windows, and `is_available` false is a low balance, not a bad key.

- OpenCode Go usage reader: 50 product lines added, 5 removed, plus 54 test lines. It serves `engine/model` from `/zen/go/v1/usage` with no workspace header. `rate-limited` counts as 100%, and a 403 means no Go subscription, so it reads as unknown and not as a bad key.

- Meta usage reader: 33 product lines added, plus 52 test lines. It serves `engine/model` from the signals the #6467 patch records, with no request, because Meta has no usage endpoint. An account stays unknown until it serves a request. A window past its reset is stale, so it drops out rather than reading 0.

- Removal of the passive Codex quota signals from `DynaCredentials`: 40 product lines and a test removed from the patch above. `engine/model` now reads every provider through `auth-files/quota`, so nothing reads them.

## Pulls

- `v8.0.49` (2026-11-01): `git subtree pull` merged with no conflicts. Every patch above still applies, and none is obsolete: PR #5434 stays open and unmerged, PR #5833 was closed unmerged. The release changes plugin login, Gemini sanitizing and Codex commentary through Claude clients. The commentary option defaults to off, so `engine/model` sees no change in routing, management paths or credential handling. `TestResponsesInterruptStopsHTTPUpstream` and `TestCodexClientModelsApplyPatchRouting` are flaky on `v8.0.48` with Dyna's patches too, so this pull did not cause them; CI does not run them.

## Update

Run this command from the Dyna repository root, replacing `<tag>` with the new upstream tag:

```sh
git subtree pull --prefix=next/third_party/cliproxyapi https://github.com/router-for-me/CLIProxyAPI <tag> --squash
```

Keep the subtree commits in the Dyna history. Do not rebase or squash them away. Resolve any conflicts with Dyna's patches, then build and test the v2 engine.

## Verify the tree

The vendored files must equal the upstream tag, except for `DYNA.md` and any patches listed above. Run this from the Dyna repository root to compare the trees:

```sh
d=$(mktemp -d) && mkdir "$d/upstream" "$d/vendored" && git archive 250d598175db61a4e99ac38b9164ed04745754e5 | tar -x -C "$d/upstream" && git archive HEAD:next/third_party/cliproxyapi | tar -x -C "$d/vendored" && diff -r -x DYNA.md "$d/upstream" "$d/vendored"; rm -rf "$d"
```

The subtree squash commit that `git subtree add` created names the upstream commit in its message. Its tree equals the upstream commit's tree.
