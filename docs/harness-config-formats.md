# Harness configuration read adapters

Research checked on 2026-10-17. The adapters read user configuration under an explicit home directory. They never start a harness, connect to an MCP server, evaluate substitutions, or write configuration. Missing files return an empty list. Invalid files return an error that excludes their contents.

## Capabilities and default paths

All catalog harnesses support MCP. `ConfigCapabilities()` returns `harness`, `mcp` and optional `mcpReason`. Unsupported capabilities return an empty list through the shared readers. An unsupported capability must have a clear reason. The skill catalog is global and is not a harness config capability.

| Catalog ID | User MCP file | Root key and format | Harness-native skill directory (Dyna does not write it) |
| --- | --- | --- | --- |
| `claude-code` | `~/.claude.json` | `mcpServers`, JSON | `~/.claude/skills/` |
| `pi` | `~/.pi/agent/mcp.json` | `mcpServers`, JSON | `~/.pi/agent/skills/` |

Adapters read a command string and a separate arguments array. Remote servers use `url`. Readers expose environment variable names, never their values. They exclude authentication headers. Hashes cover the complete parsed entry so changes to secret values and unknown fields still show drift. Dyna reads no personal skill definition. The global skill catalog names a directory with a `SKILL.md`, and Dyna copies nothing from it into a harness.

## Extra adapter evidence

Pi now has native MCP support. Do not classify it as unsupported based on older releases.

- [Pi MCP source documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md)
- [Pi skill source documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md)
- Local checks: Pi 1.1.0 includes `docs/mcp.md` and `docs/skills.md`. `pi mcp --help` identifies the user file. `pi --help` lists `--no-mcp`, `--skill`, and `--no-skills`.

## Scope and limits

These are default user-file readers, not a reconstruction of every harness runtime layer. Process environment overrides, project configuration, enterprise policy, plugin inventories, package resources, and runtime skill activation are outside this API. For example, `GROK_HOME`, `PI_CODING_AGENT_DIR`, `XDG_CONFIG_HOME`, and `CODEX_HOME` can change runtime paths. The readers intentionally use the supplied home so imports can inspect a selected home safely.

Disabled server definitions remain visible as configured entries. Readers do not claim that listed servers can connect. They do not execute Pi `!command` values or expand environment references.

Tests use temporary HOME fixtures only. They compare file bytes, paths, modes, and modification times before and after successful, missing-file, and malformed-file reads. No real sync runs as part of this work.
