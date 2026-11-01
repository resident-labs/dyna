# Dyna's CLIProxyAPI vendor

Dyna vendors this source for the v2 engine. The upstream repository is `https://github.com/router-for-me/CLIProxyAPI`. This subtree starts at tag `v8.0.48`, commit `dbb706215a3afd2f0ea3c96482d4233856466caa`. The module path is `github.com/router-for-me/CLIProxyAPI/v8`. The upstream MIT license and all upstream notices remain unchanged.

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
- Private credential discovery and typed runtime status: 83 product lines added. The SDK exposes pathless credentials and bounded numeric quota signals to the local adapter. `RequiresSignIn` is runtime-only, so an unverified credential stays unknown. Upstream issue #4585 confirms file-path listing gaps; #6201 remains open, and no checked upstream patch covers this adapter.

- Atomic OAuth grant replacement for existing credentials: 69 product lines added and 4 removed, plus regression tests. It uses the existing per-auth mutation gate and registration epoch to discard refreshes from an older grant, while preserving upstream partition merges. The existing request-info seam carries raw new-grant identity captured before upstream merges old metadata, so Dyna can reject a missing or different identity without skipping that merge. `engine/model` uses it for safe account reconnect. The upstream search on 2026-10-31 found issue #6288 on retry and #4945 on relogin metadata, but no matching atomic refresh-versus-login patch; upstream #4734 addresses targeted Codex recovery.

- Request-scoped credential eligibility: the engine sends encoded bound credential IDs over its private proxy socket; the handler removes the header and captures the set in context. Local selection, affinity and retries must stay within that set. The 2026-10-31 upstream search found issue [#6972](https://github.com/router-for-me/CLIProxyAPI/issues/6972) for external single-ID dispatch and PR [#5660](https://github.com/router-for-me/CLIProxyAPI/pull/5660) for provider prefixes, but neither implements a request-local multi-credential allowlist.

- Force-prefix isolation: remove the exception that registered a bare model when its ID equaled the credential prefix. `engine/model` uses this to isolate provider and billing namespaces. Upstream PR #5350 hides bare names from listings but keeps them routable, so it does not meet this security boundary. Drop this patch when upstream force-prefix mode never registers bare names.

- Pinned plan catalog bridge: expose only embedded plan model IDs, without reading mutable updaters, so Dyna can intersect its Pi catalog. Upstream issue [#607](https://github.com/router-for-me/CLIProxyAPI/issues/607) addresses SDK internal dependencies; no existing upstream PR or recent export through upstream commit `dbb706215a3a` supplies immutable plan IDs. Remove this bridge when upstream provides that export.

- Activation namespace: set the credential prefix within staged activation before model registration. `cmd/dyna` uses this for plan and key isolation. The upstream credential activation search found no fitting staged activation patch; #6715 adds a login route rather than this Dyna handoff. Drop this patch with the staged activation bridge.

## Update

Run this command from the Dyna repository root, replacing `<tag>` with the new upstream tag:

```sh
git subtree pull --prefix=next/third_party/cliproxyapi https://github.com/router-for-me/CLIProxyAPI <tag> --squash
```

Keep the subtree commits in the Dyna history. Do not rebase or squash them away. Resolve any conflicts with Dyna's patches, then build and test the v2 engine.

## Verify the tree

The vendored files must equal the upstream tag, except for `DYNA.md` and any patches listed above. Run this from the Dyna repository root to compare the trees:

```sh
d=$(mktemp -d) && mkdir "$d/upstream" "$d/vendored" && git archive dbb706215a3afd2f0ea3c96482d4233856466caa | tar -x -C "$d/upstream" && git archive HEAD:next/third_party/cliproxyapi | tar -x -C "$d/vendored" && diff -r -x DYNA.md "$d/upstream" "$d/vendored"; rm -rf "$d"
```

The subtree squash commit that `git subtree add` created names the upstream commit in its message. Its tree equals the upstream commit's tree.
