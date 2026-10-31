# Dyna's CLIProxyAPI vendor

Dyna vendors this source for the v2 engine. The upstream repository is `https://github.com/router-for-me/CLIProxyAPI`. This subtree starts at tag `v8.0.48`, commit `dbb706215a3afd2f0ea3c96482d4233856466caa`. The module path is `github.com/router-for-me/CLIProxyAPI/v8`. The upstream MIT license and all upstream notices remain unchanged.

## Dyna changes

P0 adds this documentation and changes no upstream CLIProxyAPI file. It changes no proxy behavior. The folder has its own `go.mod`, so the v1 root module and `go test ./...` skip it.

The v1 gofmt check, `scripts/ci-gofmt.sh`, skips this folder. One upstream test file does not pass gofmt, and Dyna keeps upstream formatting unchanged. This is a change to Dyna's script, not to the vendored source.

Each later patch to this folder must be its own commit. Each patch must add an entry here that names its reason and the Dyna code it serves.

- The SDK builder accepts an already-bound listener. `engine/model` uses it to avoid a loopback port reservation race that could expose the client key.
- Upstream PR [#5434](https://github.com/router-for-me/CLIProxyAPI/pull/5434), head `fa29f0716067730b6aedbf3e3602ad7be489f9b7`, adds normalized Claude OAuth quota reports for `engine/model`. The patch changes upstream v7 import paths to v8; drop it when a subtree update includes the upstream change. Patch size: 812 lines added and 15 removed, including tests. The upstream body-cap test passed in 0.02 seconds on hermeslab.

- Preserve `seven_day_oauth_apps` alongside nonempty `limits` when the normalized limits omit that window. `engine/model` uses it for the correct Claude weekly quota. This separate patch adds 38 lines and removes 3, including its regression test; drop it when upstream fixes mixed responses.
- OAuth in-memory handoff and adoption status: 124 product lines added, 4 removed. Dyna enables it for Claude and Codex callbacks, and it exposes adopted credential IDs for the supported flows. Upstream PR #4800 handles a busy callback port, not secret-free handoff; issue #295 asks for manual callbacks, not this storage boundary.

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
