# Harnesses page

Open **Settings → Harnesses** in Dyna desktop, the paired web app, or the mobile web app. The same Svelte components call the same Go service through Wails or the authenticated web and Connect API.

The page manages Dyna's private copy of each catalog harness: Codex, Claude Code, Grok, OpenCode v2, Antigravity, Pi and Muse. For each harness it installs and selects exact versions, signs in, switches and removes accounts, and shows the doctor status. The **Tools** tab edits the managed MCP servers and skills.

Dyna does not use or change a global harness binary on PATH. See [private harness runtimes](../private-harness-runtimes.md) for bundles, homes, accounts and first-use install.

## Boundaries

- `internal/engine/calls.go` binds the private runtime service. Every mutation obeys read-only mode and the paired-device or Connect authentication gates.
- `internal/engine/calls.go` also binds the harness config catalog (MCP servers, skills and tool bridges).
- `web/src/lib/settings/harnesses/` contains the pane, its state helpers and the bundled brand icons. Icon provenance and the T3 Code MIT notice are beside the icons.

Old releases repaired global installations and saved backups under the harnesses data directory's `backups/`. `dyna harness doctor` still lists those manifests for manual recovery.
