---
name: dyna-extend
description: Use when you need to add a skill or mcp server to dyna
---

# Extend Dyna

Dyna keeps three kinds of add-on. Pick the one that fits. The `dyna` command works in this box, and the `skill`, `package` and `mcp` tools do the same.

| Kind | Command | Use it for |
|---|---|---|
| Skill | `dyna skill save PATH [--global]` | Instructions an agent reads when they apply, with any files they need |
| Pi package | `dyna package add SOURCE [--global]` | Tools, skills and prompts that Pi installs from npm or git |
| MCP server | `dyna mcp add NAME URL_OR_COMMAND [--global]` | Tools served from outside Pi |

Each kind also has `list` and `remove NAME`.

## Where it goes

Add to the room unless the owner tells you otherwise: leave out `--global`. The owner may not want something global or version controlled until they say so.

- **Room**, the default: every session in this room. It needs no approval.
- **Global**, with `--global`: every room. The request waits for the owner, and you hear the result.
- **Repository**: a skill at `.pi/skills/<name>/SKILL.md` or `.agents/skills/<name>/SKILL.md` belongs to that repository. Pi loads it by itself, so Dyna's commands do not touch it, and it changes by commit and PR. Put a skill there only when the owner asks.

An item reaches a session at its next box start, not the box that is running now. If a package fails to install or a server fails to connect, the session still starts and a notice names what failed.

## Create a skill

A skill is a folder with a `SKILL.md`:

```markdown
---
name: pdf-tools
description: Extract text from PDF files. Use when reading or converting a PDF.
---

Run `scripts/extract.sh FILE`, then read its output.
```

Use lowercase letters, digits and hyphens for the name. Every prompt carries the description and nothing else, so say what the skill does and when to use it. An agent reads the rest when it applies. Keep long references in files beside `SKILL.md` and name them by relative path.

Save the folder with `dyna skill save ./pdf-tools`. PATH can also be a link to a `SKILL.md` or to a GitHub folder. Saving copies the files, so save again after an edit.

## Add a Pi package

A Pi package is an npm package or git repository that Pi installs. It can bundle extensions, which run as tools, with skills and prompts, and it carries its own dependencies. A skill is only text and files. Use a package when something has to run as a Pi tool, or when someone already publishes it as one.

```bash
dyna package add npm:@example/pi-tools
dyna package add https://github.com/example/pi-tools
```

The source is pinned when you add it, to an npm version or a git commit, so every box gets the same thing. Add it again to move to a newer one.

## Add an MCP server

```bash
dyna mcp add docs https://example.com/mcp
dyna mcp add files -- npx -y @modelcontextprotocol/server-filesystem /box/work
```

The server joins Pi's MCP config beside `dyna` and `shell`. Call its tools from `codemode` as `tools.mcp__<name>__<tool>`. Dyna has no store for an MCP server's secrets, so if a server needs a token, tell the owner instead of writing it down.
