# Dyna's Pi fork

Dyna modified this vendored source. The upstream repository is `https://github.com/earendil-works/pi.git`. This subtree starts at tag `v1.4.0`, commit `4a080252f06caf63a525d7873c5d94d3c23887e0`. The upstream MIT license and all package license notices remain unchanged.

## Dyna changes

P0 adds this documentation and changes no upstream Pi file. It changes no Pi behavior and removes no Dyna code. `scripts/pi-pack.sh` stages the built packages outside this tree, sets every internal fork package to `1.4.0-dyna.1`, and adds `dynaFork: 1` and bundled dependencies to the staged metadata. The upstream files stay unchanged.

Each later Pi behavior change must add an entry here and name the Dyna code it removes.

P1 adds `--json` to `--list-models` (`packages/coding-agent/src/cli/args.ts`, `src/cli/list-models.ts`, `src/main.ts`): one JSON array on stdout with `provider`, `id`, `name`, exact `contextWindow` and `maxTokens`, `reasoning`, and the `thinkingLevels` that `--thinking` accepts for each model. The text table is unchanged without `--json`. The pack version rises to `1.4.0-dyna.2` with `dynaFork: 2`. It lets Dyna delete the `--list-models` table parser and `piTokens` in `internal/account/service/adapters/pi.go`, which read rounded sizes; Dyna now decodes this JSON and takes efforts from `thinkingLevels`.

P2 adds the `compaction.triggerTokens` setting (`packages/coding-agent/src/core/settings-schema.ts`, `src/core/settings-manager.ts`, `src/core/compaction/compaction.ts`): an optional absolute token count, global and per model under `compaction.modelOverrides`, resolved like `reserveTokens`. Automatic compaction starts above `min(triggerTokens, contextWindow - reserveTokens)`; unset keeps the reserve-based rule. The `--compaction-trigger-tokens <n>` flag (`src/cli/args.ts`, applied in `src/core/agent-session.ts` via `withCompactionTriggerOverride`) sets the trigger for one process and wins over both the setting and model overrides. The pack version rises to `1.4.0-dyna.5` with `dynaFork: 5`. It lets Dyna delete `internal/harness/runtime/pi_compaction.go` and the `compaction.modelOverrides` reserve injection in `generation.go`, which computed per-model reserves from rounded windows and missed models absent from the model list. Dyna passes the profile's compaction target per invocation as `--compaction-trigger-tokens` (`internal/harness/grok_pi.go`), so profiles sharing one account cannot overwrite each other's published settings and resumed sessions keep their own trigger; the generation writes nothing for compaction. Dyna requires fork level 5 from this change: a level 3 binary rejects the flag.


P4 adds `PI_NO_ENV_CREDENTIALS` (`packages/coding-agent/src/main.ts`). Provider auth definitions declare the environment variables they read for credentials and endpoints, and the CLI deletes those variables before provider resolution or child-process startup. Stored credentials and `--api-key` remain available. The pack version rises to `1.4.0-dyna.3` with `dynaFork: 3`. It lets Dyna delete `piProviderEnv` and its clearing loop in `internal/harness/runtime/adapter_pi.go`; Dyna now sets only `PI_NO_ENV_CREDENTIALS=1`, and Pi's own registry covers new providers.

P6 makes `--session <id>` in RPC, JSON and print modes open a session from another project without asking whether to fork. A missing id prints `session not found: <id>` and exits with code 2 before runtime creation; interactive mode keeps its prompt. The pack version rises to `1.4.0-dyna.4` with `dynaFork: 4`. It lets Dyna delete the session-path rewrites in `internal/harness/grok_pi.go` and the `resumeReady` filesystem precheck in `internal/harness/harness.go`. `PiSessionPath` remains for session conversion in `internal/subagent`, which still reads the session file.

P5 streams codemode script output (`packages/codemode/src/types.ts`, `src/runtime/execution.ts`, and `packages/coding-agent/src/extensions/codemode/execute.ts`): the sandbox reports each output item, and codemode partial results contain only the running header and a bounded preview of script output. Nested call previews stay off updates; the final tool result message keeps `nestedCalls` with full arguments. The pack version rises to `1.4.0-dyna.6` with `dynaFork: 6`. It lets Dyna delete the codemode `tool_execution_update` filter in `internal/workeroutput/pi.go`, so script output streams like other tool output.

P3 adds the per-process config file flags `--settings <file>`, `--mcp-config <file>` and `--models-file <file>` (`packages/coding-agent/src/cli/args.ts`, applied in `src/main.ts` through the agent file overrides in `src/config.ts`, read by `src/core/settings-manager.ts`, `src/extensions/mcp/config.ts`, `src/core/agent-session-services.ts` and `getModelsPath`): each flag replaces exactly that agent-directory file for one process, a path that does not exist is a startup error, and project settings and trusted project MCP configuration still merge on top. `auth.json` and sessions always come from the agent directory. The pack version rises to `1.4.0-dyna.7` with `dynaFork: 7`. It lets Dyna delete `publishPiAccountHome`, `copyPiModels` and the `.dyna-generation` marker in `internal/harness/runtime/pi_home.go`, the `pi-mcp.json` publish into the shared home in `generation_lifecycle.go`, and the `set_auto_retry` startup command in `internal/harness/pi_rpc.go`: Dyna passes the generation's own `settings.json` and `pi-mcp.json` and, when the owner defined models, the shared `models.json` per invocation (`piConfigArgs` in `adapter_pi.go`), and turns Pi's own retry off in the generation settings (`piRetrySettings` in `generation.go`), so generations sharing one harness home never overwrite each other's files and no account home holds files Dyna manages. `--models-file` is spelled out because upstream `--models` already names model patterns for cycling.

## Fork versions

From F1 on, the version names one build: `1.4.0-dyna.<level>.<build>`, where `<build>` is the first twelve hex characters of the `third_party/pi` tree hash and `dynaFork` stays the level. Two PRs at one level can land side by side, because each build installs as its own version. Raise the level only when Dyna starts to depend on a new fork feature: then raise `piForkVersion`, `minimumPiForkLevel` and the `dynaFork` in `scripts/pi-pack.sh` together. A change that needs no new Pi feature keeps main's level. Dyna gates readiness on the level alone (`internal/harness/runtime/version_range.go`), never on build ordering. Release and self-update stamp the build hash into the binary (`piForkBuild`), and an unstamped development binary reads it from the tarball named by `DYNA_PI_TARBALL`. First use installs and selects in one step when the selected bundle is unusable, recording the replaced selection as the rollback target (`internal/harness/runtime/firstuse.go`).

## Update

Run this command from the Dyna repository root, replacing `<tag>` with the new upstream tag:

```sh
git subtree pull --prefix=third_party/pi https://github.com/earendil-works/pi <tag> --squash
```

Keep the subtree commits in the Dyna history. Do not rebase or squash them away. Resolve any conflicts with Dyna's changes, then run the Pi build and coding-agent tests.

## Pack and install a development build

Run `scripts/pi-pack.sh /absolute/path/pi-coding-agent.tgz`. The version inside the tarball is `1.4.0-dyna.<level>.<build>`. The script hydrates the pinned model catalog, builds offline, and writes one tarball with all internal `@earendil-works/*` runtime packages bundled. npm treats `bundleDependencies` as complete, so the script also strips the bundled packages' own dependency lists and merges every third-party package they need into the top-level manifest, pinned to the versions the vendored lockfile resolved. Third-party dependencies still come from npm.

The script then verifies its own output: it installs the tarball into a temporary prefix the way Dyna does, compares the installed package set with the upstream npm package, runs `pi --version`, and executes one codemode script with no network. Set `PI_PACK_SKIP_BUILD=1` to reuse an existing install and build; the CI pi job does that.

Self-update caches the tarball under `<data dir>/pi-fork/` by source tree and build script hashes. It stamps the local path and SHA-512 integrity into the binary and keeps at least two tarballs. It also keeps every tarball referenced by the installed binary or its retained backups. Release binaries use a stamped GitHub release asset URL and integrity. For QA, set `DYNA_PI_TARBALL` to an absolute path to override either source. An unstamped development binary needs this override.

## Review

The subtree squash commit records the upstream commit. Apart from `DYNA.md`, the vendored files in P0 equal upstream `v1.4.0`. Reviewers can inspect the subtree commit instead of reading every vendored line.
