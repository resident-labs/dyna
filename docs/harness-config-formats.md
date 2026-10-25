# Harness configuration read adapters

Research checked on 2026-10-17. The adapters read user configuration under an explicit home directory. They never start a harness, connect to an MCP server, evaluate substitutions, or write configuration. Missing files return an empty list. Invalid files return an error that excludes their contents.

## Capabilities and default paths

All seven catalog harnesses support MCP and skills. `ConfigCapabilities()` returns `harness`, `mcp`, `skills`, and optional `mcpReason` and `skillsReason`. Unsupported capabilities return an empty list through the shared readers. An unsupported capability must have a clear reason.

| Catalog ID | User MCP file | Root key and format | Native user skills |
| --- | --- | --- | --- |
| `claude-code` | `~/.claude.json` | `mcpServers`, JSON | `~/.claude/skills/` |
| `codex` | `~/.codex/config.toml` | `mcp_servers`, TOML | `~/.agents/skills/` |
| `opencode` | `~/.config/opencode/opencode.json` and `.jsonc` | `mcp`, JSONC | `~/.config/opencode/skills/` |
| `grok` | `~/.grok/config.toml` | `mcp_servers`, TOML | `~/.grok/skills/` |
| `pi` | `~/.pi/agent/mcp.json` | `mcpServers`, JSON | `~/.pi/agent/skills/` |
| `muse` | `~/.config/muse/settings.json` | `mcpServers`, JSON; legacy `mcp_servers` accepted | `~/.config/muse/skills/` |

OpenCode local commands use arrays. The other adapters read a command string and a separate arguments array. Remote servers use `url`. Readers expose environment variable names, never their values. They exclude authentication headers. Hashes cover the complete parsed entry so changes to secret values and unknown fields still show drift. Skill hashes cover the exact `SKILL.md` bytes.

## Extra adapter evidence

Grok Build uses TOML. The adapter also reads shared `.agents` and Claude user skills after native skills.

- [Grok settings, paths, and TOML example](https://docs.x.ai/build/settings)
- [Grok MCP servers](https://docs.x.ai/build/features/mcp-servers)
- [Grok skills and compatibility](https://docs.x.ai/build/features/skills-plugins-marketplaces)
- Local check: `grok mcp add --help` identifies `~/.grok/config.toml` as the user file. Installed package: `@xai-official/grok` 1.0.62.

Pi now has native MCP support. Do not classify it as unsupported based on older releases. The adapter also reads shared `.agents` user skills.

- [Pi MCP source documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md)
- [Pi skill source documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md)
- Local checks: Pi 1.1.0 includes `docs/mcp.md` and `docs/skills.md`. `pi mcp --help` identifies the user file. `pi --help` lists `--no-mcp`, `--skill`, and `--no-skills`.

Muse accepts either MCP root spelling. The adapter rejects a file that contains both. It reads native and shared `.agents` skills, then Claude and Codex user skills. `context.foreign_personal_skills: false` excludes the latter two roots.

- [Muse MCP settings and legacy spellings](https://meta-models.github.io/muse-code-sdk/next/guides/extend/mcp-servers/)
- [Muse skill roots and compatibility switch](https://meta-models.github.io/muse-code-sdk/next/guides/extend/skills/)
- Local checks: `muse --help`, `muse config --help`, and `muse skills --help` confirm settings validation, MCP OAuth, and skill management.

## Scope and limits

These are default user-file readers, not a reconstruction of every harness runtime layer. Process environment overrides, project configuration, enterprise policy, plugin inventories, package resources, and runtime skill activation are outside this API. For example, `GROK_HOME`, `PI_CODING_AGENT_DIR`, `XDG_CONFIG_HOME`, and `CODEX_HOME` can change runtime paths. The readers intentionally use the supplied home so imports can inspect a selected home safely.

Disabled server definitions remain visible as configured entries. Readers do not claim that listed servers can connect. They do not execute Pi `!command` values or expand environment references. Muse requires `schema_version: 1` at runtime; this import reader extracts MCP definitions without validating unrelated settings.

Tests use temporary HOME fixtures only. They compare file bytes, paths, modes, and modification times before and after successful, missing-file, and malformed-file reads. No real sync runs as part of this work.
