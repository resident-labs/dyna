# Dyna's Pi fork

Dyna modified this vendored source. The upstream repository is `https://github.com/earendil-works/pi.git`. This subtree starts at tag `v1.4.0`, commit `4a080252f06caf63a525d7873c5d94d3c23887e0`. The upstream MIT license and all package license notices remain unchanged.

## Dyna changes

P0 adds this documentation and changes no upstream Pi file. It changes no Pi behavior and removes no Dyna code. `scripts/pi-pack.sh` stages the built packages outside this tree, sets every internal fork package to `1.4.0-dyna.1`, and adds `dynaFork: 1` and bundled dependencies to the staged metadata. The upstream files stay unchanged.

Each later Pi behavior change must add an entry here and name the Dyna code it removes.

P1 adds `--json` to `--list-models` (`packages/coding-agent/src/cli/args.ts`, `src/cli/list-models.ts`, `src/main.ts`): one JSON array on stdout with `provider`, `id`, `name`, exact `contextWindow` and `maxTokens`, `reasoning`, and the `thinkingLevels` that `--thinking` accepts for each model. The text table is unchanged without `--json`. The pack version rises to `1.4.0-dyna.2` with `dynaFork: 2`. It lets Dyna delete the `--list-models` table parser and `piTokens` in `internal/account/service/adapters/pi.go`, which read rounded sizes; Dyna now decodes this JSON and takes efforts from `thinkingLevels`.

P4 adds `PI_NO_ENV_CREDENTIALS` (`packages/coding-agent/src/main.ts`). Provider auth definitions declare the environment variables they read for credentials and endpoints, and the CLI deletes those variables before provider resolution or child-process startup. Stored credentials and `--api-key` remain available. The pack version rises to `1.4.0-dyna.3` with `dynaFork: 3`. It lets Dyna delete `piProviderEnv` and its clearing loop in `internal/harness/runtime/adapter_pi.go`; Dyna now sets only `PI_NO_ENV_CREDENTIALS=1`, and Pi's own registry covers new providers.

## Update

Run this command from the Dyna repository root, replacing `<tag>` with the new upstream tag:

```sh
git subtree pull --prefix=third_party/pi https://github.com/earendil-works/pi <tag> --squash
```

Keep the subtree commits in the Dyna history. Do not rebase or squash them away. Resolve any conflicts with Dyna's changes, then run the Pi build and coding-agent tests.

## Pack and install a development build

Run `scripts/pi-pack.sh /absolute/path/pi-coding-agent-1.4.0-dyna.3.tgz`. The script hydrates the pinned model catalog, builds offline, and writes one tarball with all internal `@earendil-works/*` runtime packages bundled. npm treats `bundleDependencies` as complete, so the script also strips the bundled packages' own dependency lists and merges every third-party package they need into the top-level manifest, pinned to the versions the vendored lockfile resolved. Third-party dependencies still come from npm.

The script then verifies its own output: it installs the tarball into a temporary prefix the way Dyna does, compares the installed package set with the upstream npm package, runs `pi --version`, and executes one codemode script with no network. Set `PI_PACK_SKIP_BUILD=1` to reuse an existing install and build; the CI pi job does that.

Self-update caches the tarball under `<data dir>/pi-fork/` by source tree and build script hashes. It stamps the local path and SHA-512 integrity into the binary and keeps at least two tarballs. It also keeps every tarball referenced by the installed binary or its retained backups. Release binaries use a stamped GitHub release asset URL and integrity. For QA, set `DYNA_PI_TARBALL` to an absolute path to override either source. An unstamped development binary needs this override.

## Review

The subtree squash commit records the upstream commit. Apart from `DYNA.md`, the vendored files in P0 equal upstream `v1.4.0`. Reviewers can inspect the subtree commit instead of reading every vendored line.
