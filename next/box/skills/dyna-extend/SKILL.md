---
name: dyna-extend
description: Use when sessions need a skill, Pi package or MCP server they lack, a procedure is worth saving as a skill, or an added one must change, turn off, become root only or go.
---

# Add skills, Pi packages and MCP servers

An add-on reaches every session of a room, or of every room, at its next box start. Pick the kind
by what it has to do: a skill is text and files an agent reads, a Pi package runs code as Pi tools,
and an MCP server serves tools from a process or a URL. Each kind has a tool (`skill`, `package`,
`mcp`) and the same `dyna` command in your box.

| Need | Kind | Add | Check |
|---|---|---|---|
| A procedure or reference, with its scripts | skill | `dyna skill save PATH` | `dyna skill list` |
| Tools someone publishes for Pi, on npm or git | Pi package | `dyna package add SOURCE` | `dyna package list` |
| Tools an MCP server serves | MCP server | `dyna mcp add NAME URL_OR_COMMAND` | `dyna mcp list` |

Each kind's `remove NAME` undoes a new add; with `--global` it waits for a decision as an add does.

## Choose where it goes

- **Your room**, the default: no flag, no approval. Add to the room
  unless the owner tells you otherwise. A room item wins a name clash with a global one.
- **Every room**, `--global`: the request waits for a decision by the room's root, or by the
  owner when the root's dyna_room section says so (root: dyna-admin).
- **Root only**, `--root-only` on save or add: only the root session's box gets it, and other
  sessions' `list` does not show it. Saving the same name again keeps the mark.
- **One repository**: a skill at `.pi/skills/<name>/SKILL.md` or `.agents/skills/<name>/SKILL.md`
  loads in boxes on that repository by itself. It changes by commit and PR, and Dyna's commands
  do not see it. Put a skill there only when the owner asks.

A session changes only its own room.

## Make it reach sessions now

Nothing reloads a running box. After an add or change, call `session refresh` on the sessions that
need it, then check: a skill appears in the skills list of the prompt; an MCP server's tools are named
`mcp__<name>__<tool>` in deferred_tools; a package or server that failed arrives as a notice
naming it, and the session runs without it.

## Create a skill

A skill is a folder with `SKILL.md` and any files it names by relative path:

```markdown
---
name: pdf-tools
description: Use when you must read or convert a PDF file and plain text tools show nothing.
---

Run `scripts/extract.sh FILE`, then read its output.
```

- `name` is lowercase letters, digits and single hyphens, up to 64 characters; it, not the
  folder's name, names the skill. A built-in skill's name (`built-in` in `dyna skill list`) is
  refused with `NAME is a built-in skill; pick another name`: built-ins cannot be saved over.
- Every prompt carries the description, up to 1024 characters; the body is read only when the
  description matches.

Write it to the standard Dyna's own skills meet:
1. The description says when to open it, in the words of the situation: "Use when …", the
   symptom, the error text or the task. Under 300 characters; not a list of contents.
2. Two or three opening sentences say what the thing is; the headings are the reader's tasks.
3. Every command is exact and copied from a run or from `--help`; never invent a flag.
4. For each change: how to read the current value, change it, check it and undo it, and who may.
5. Say only the non-obvious (defaults, limits, costs, silent failures), nothing a tool
   description or the system prompt already says, in under 150 lines.

Save it with `dyna skill save ./pdf-tools`. PATH is a folder, a single `SKILL.md` file (saved
alone), or a link: a GitHub link to a repository or folder brings the whole folder, and any other
link must be one `SKILL.md`. Saving copies the files, so save again after each edit; remove it
with `dyna skill remove pdf-tools`.

## A skill whose commands run on a host

When the tool a skill drives lives on another machine, give its run-on hosts:
`dyna skill save ./desk-tools --host HOST`, with `--host` repeated for several. Each must be a
registered host's name, or the save fails with `no host named HOST`. Boxes then see
` · runs on HOST` after the description and run its commands with bash's host set; a session
needs a host grant for that. Nothing is installed on the host. Saving again without `--host`
keeps the hosts; with `--host` it replaces them.

## Change an item after adding it

These are owner methods, refused from a box: the room's root runs them with the owner CLI on the
engine host (dyna-admin); any other session asks its parent. Give `--room ROOM` for a room's
item and leave it out for a global one.

| Change | Command |
|---|---|
| Set a skill's run-on hosts without saving its files | `dyna skill set NAME --host HOST --room ROOM` |
| Clear them | `dyna skill set NAME --room ROOM` |
| Copy a skill folder from a registered host | `dyna skill save HOST:PATH --room ROOM` |
| Turn an item off, or back on | `dyna addon set skill NAME --room ROOM --enabled off` |
| Make an item root only, or not | `dyna addon set mcp NAME --room ROOM --root-only on` |

KIND in `addon set` is `skill`, `package` or `mcp`. Check with the kind's `list`, whose columns are
name, scope, on or off (with "root only"), who added it, and its source.

## Replace an item, and undo it

Saving or adding a name that exists replaces that item in the same scope: its root-only mark
stays, a skill saved without `--host` keeps its hosts, and an item turned off is on again. Dyna
keeps no old version, so record it first:

- **Skill**: keep a copy of the folder you last saved, and note its scope and hosts from
  `dyna skill list`. Undo by saving that copy again in the same scope.
- **Package**: note its pinned source from `dyna package list`, then add the new source. Undo by
  adding the noted source again.
- **MCP server**: note its scope and URL or command from `dyna mcp list`. Add it again with the
  same name and scope and the new URL or command; do not remove it first. Undo by adding the
  noted definition again.
- **Run-on hosts**: note them from `dyna skill list` before `skill set`; undo with `skill set`
  and the old hosts.

After a change or an undo, refresh and check as in "Make it reach sessions now".

## Add a Pi package

Use one when something has to run as a Pi tool, or is already published as a package:

```bash
dyna package add npm:@example/pi-tools
dyna package add https://github.com/example/pi-tools
```

An npm range is refused; give an exact version or a tag. To move to a newer version, add it again
(see "Replace an item"). `package list` gives the name `remove` takes.
The box has no Pi documentation. A package declares what it bundles in the `pi` block of its
`package.json`, each key a list of files, folders or globs:

```json
{ "pi": { "extensions": ["extensions/*"], "skills": ["skills/*"], "prompts": ["prompts/*.md"], "themes": ["themes/*.json"] } }
```

## Add an MCP server

```bash
dyna mcp add docs https://example.com/mcp
dyna mcp add files -- npx -y @modelcontextprotocol/server-filesystem /box/work
```

Put `--` before a command with flags of its own. A command runs inside each box, so the room's
image must have it (dyna-images). Use 1 to 64 letters, digits, underscores or hyphens; no leading,
trailing or double underscore. The name `dyna` is reserved.
Dyna has no place for a server's secret: a token in a URL or argument is stored as plain text that
`mcp list` prints, so ask the owner instead of adding one.
