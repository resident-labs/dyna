# Private harness runtimes

Every profile with a catalog harness runs on Dyna's private copy of that harness. An optional exact `runtimeVersion` in the profile pins a version. Older releases also stored a `runtime` field (`system` or `dyna`). Dyna ignores it, so those profiles run on the private copy too. Custom and mock harnesses run their own command, and the doctor reports them as `direct`. The CLI, the Harnesses page and the desktop API manage private bundles, saved logins, the active login and MCP servers. The skill catalog is global; enabled skills reach every session through its standing instructions. The doctor reports the selected binary, native home, account, and remaining personal reads.

Install the latest verified version with `dyna harness install <harness>`, or an exact version with `--version <version>`. `dyna harness list` shows installed bundles. `dyna harness doctor` shows runtime status. Supported management adapters are Claude Code and Pi. Pi uses its native structured output format for worker turns.

An update never selects a version it has not run: Dyna starts the new version once in a throwaway private scope and requires it to answer its version command, with no provider calls. A version that cannot start, stays silent, or reports a version outside the harness's supported range is not selected. When an update does switch the selection, Dyna records the replaced version as the last good version and keeps it on disk. `dyna harness rollback <harness>` re-selects that last good version in one command.

Each harness has one active login and one shared native home. Saved account slots contain login files. Native history stays in the shared home. `dyna harness login <harness>` starts native sign-in. `dyna harness accounts activate <account-id>` switches accounts. A switch blocks new turns and waits for running turns to finish. Claude Code allows concurrent turns. Other adapters serialize turns until native concurrency is verified.

Every private runtime, including Claude Code, uses its own HOME and XDG roots. The native scope stays at `accounts/<harness>/.runtime` under the Dyna data directory. HOME is the paired directory `.runtime.home`. Its `.config`, `.local/share`, `.cache`, and `.local/state` directories supply the four XDG roots. Saved-slot login uses a paired `<account-id>.home`. These directories have mode `0700`. Credential checks inspect native files and known auth paths; they do not walk tool caches. Tools can create sockets and symlinks in their private HOME.

Dyna never copies a personal login on its own. A turn with no private account fails with "Sign in to <harness> in Dyna." When the user clicks Sign in on the Harnesses page, or runs `dyna harness login <harness>`, Dyna looks for the personal login of that harness. If it finds one, the user chooses: use it (Dyna copies the login once) or sign in instead. `--use-personal` and `--sign-in` answer that question for scripts. Dyna offers the copy only for a harness with no saved account, and never for a login that is already saved. The copy takes login data only. Personal settings, skills, hooks, plugins, and MCP definitions stay outside the private roots. It never replaces files in the private home. For Claude Code, the copy shares a refresh-token family with the personal login. A refresh on either side can require a new sign-in on the other side. The copy only reads login data and does not validate or refresh tokens. If a native refresh later fails, the user signs in again. A deadline or a failed native command fails the copy with "Copying your personal login did not finish. Try again."

Private HOME hides automatic personal skill, MCP, hook, plugin, and settings discovery. Project settings and ancestor instructions still apply. HOME isolation is not a filesystem sandbox. Personal preference import stays explicit.

Development tools keep their installed toolchains and shared build caches. Dyna pins the owner's mise, Rust, Go, npm, Bun, and pnpm roots. It preserves explicit root settings. The private Git configuration includes the owner's XDG Git configuration before `.gitconfig`. It also preserves global ignore and attributes files. The SSH agent socket and the operating system's SSH key lookup remain available. `GH_CONFIG_DIR` preserves the gh login. Dyna does not copy keys or read personal shell startup files.

Only selected tools receive the owner's HOME. Dyna installs bridges for `dyna` and `git` in the private HOME. The Git bridge and each opt-in tool bridge restore the original HOME and XDG values for that tool, including whether each variable was unset. Dyna restores them itself: every harness launch carries `DYNA_OWNER_ENV`, and each `dyna` process reads it at start. So `dyna` on PATH, `$DYNA_BIN`, an absolute path, a hook or the computer Chromium wrapper all keep room posts, journals, hooks, and nested workers on the owner's existing Dyna store. A caller that sets its own HOME or XDG root keeps it, and `dyna mcp serve` keeps the private roots for generated MCP servers. Other commands keep private roots. Add trusted tool names to the `toolHomeBridges` array in Dyna’s `harness-config.json`. For example, `"toolHomeBridges": ["web-oc", "porch", "hypruse", "jev"]` lets those helper tools use their owner configuration. Profile and inherited `DYNA_HOME_TOOLS` values cannot override this owner list. Each bridge and Git config set has an immutable directory, so a later launch cannot change the files that an earlier launch uses. Opt-in tools can read that configuration, so select them with care.

Cloud and registry directories such as `~/.aws`, `~/.docker`, and `~/.config/gcloud` are unavailable by default. Add the corresponding trusted CLI to `toolHomeBridges` when needed. The doctor lists this policy and the private tool cache path. Toolchain installs and shared build caches stay outside that private cache.

`dyna mcp` supports `list`, `add`, `rm`, `enable`, `disable`, and `sync`; MCP entries are per harness, so use `--harness` to limit them, and `sync` writes a private native config for one account. `dyna skill` supports `list`, `add`, `rm`, `enable`, and `disable` and takes no `--harness`: the skill catalog is global. Dyna never writes a skill into a harness home, a native config or a plugin. Instead, every session's standing instructions list the enabled skills with a one-line description and the absolute path of each `SKILL.md`; the agent reads the file only when the task matches. An MCP edit regenerates the affected private homes; a skill edit does not. `dyna skill show` prints the Dyna orchestrator skill. It is for agents outside Dyna: copy it into your own harness by hand. Dyna does not install it and does not add it to the catalog.

Personal preference import requires a preview and accepts only known safe settings. It excludes credentials, environment values, commands, hooks, MCP servers, skills, plugins, provider settings, and permission controls. Claude Code can import these previewed preferences. The other harnesses run with their own default settings and import none. Imported settings do not overwrite personal configuration.

Sessions pin a binary version and generated configuration. Resume keeps native history and uses the current active login. A session's enabled skills come from the global catalog through its standing instructions, so they are not part of a pinned generation. Cleanup keeps the newest two bundles, selected bundles, the last good (rollback) version, open-session pins, and active-process pins. Cleanup never removes saved accounts. Uninstall removes every Dyna copy of one harness and its selection; the next sign-in or session installs it again. Account removal requires explicit confirmation.

The HOME change leaves Claude Code and Pi native scope paths in place. Resume rebuilds an old configuration generation when it lacks private roots. Confirmed account removal also removes its paired `<account-id>.home`; it preserves shared runtime history. Exports exclude account homes, and generation cleanup does not remove them.

Bundle installation also uses temporary private HOME and XDG roots. This does not change the supervisor's environment.

Dyna does not use, install, update, repair, or roll back a global harness binary. Native private bundle management stays available. Use `dyna harness rm <harness> --version <exact-version> --confirm` to remove an unused private bundle. Profiles, open sessions, and live native turns protect the versions they use.

Old repair backups remain unchanged under the harness data directory. `dyna harness doctor` lists their manifest paths and recovery directories. Open the manifest, compare each original with its saved copy, save the current file, and restore the required file or symlink by hand. Dyna has no automatic restore command.

## Native session processes

Pi RPC and Claude stream-json use one child per Dyna
session. A detached host keeps that child across turns and engine restarts.
Each turn attaches over a private Unix socket and binds its runtime leases
again. A second active attachment to the same key is refused.

## First-use install

Dyna installs a missing harness CLI the first time it needs it. These actions
trigger the install:

- A private sign-in: `dyna harness login <harness>` or **Sign in** on the Harnesses page.
- A session or workflow turn when no private bundle exists.

The install uses the same private bundle installer as `dyna harness install`.
It picks the version from `RecommendedVersions`, which reads each vendor's
official latest-version source:

| Harness | Version source | Artifact check |
|---|---|---|
| Claude Code, Pi | npm registry `/<package>/latest` | npm `dist.integrity` |

It writes a bundle under the harness data directory. A per-harness lock lets two
sessions share one install. An install of one harness does not block another.
While an install runs, the Harnesses page shows "Installing…" and `dyna harness doctor` shows `installing`.

Without internet access, the install fails with a message that says so. Connect
and try again. The Dyna image does not contain Claude
Code, so the first use needs internet access.


## Update checks

The distribution table in `internal/harness/runtime/distribution.go` owns the
source for each harness. npm sources use the official package `latest` tag and
verify the exact version metadata.

The engine checks versions every six hours, including while the UI is closed.
Version checks run independently, so one slow vendor cannot block another.
Dyna reuses successful results for six hours and retries failed checks after one
minute. The Harnesses page **Refresh** button checks the sources immediately.
Opening the page and polling an installation use the cache.

A new version is installed in a staging directory. Dyna publishes the complete
bundle with an atomic rename after verification. A failed install leaves the
old bundle and selection intact. Running sessions keep their exact version;
updates do not overwrite the executable they use.


All installers receive the adapter's private native roots as well as private
HOME and XDG roots. Pi uses the shared npm installer with
scripts disabled and the same integrity metadata handling as other npm bundles.
