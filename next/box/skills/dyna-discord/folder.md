# A server as a folder (root and owner)

The whole server's structure lives in one private folder: `rooms/ROOM/discord/` beside the room's
AGENTS.md on the engine host (with several whole-server mappings, `discord/SERVER_ID/` each).
Never put this folder in a repository or copy it into a box. Edit it with bash on the engine host.
Three actions, for the room's root (tool) and the owner (CLI):

| Action | Tool call | Owner CLI | Writes |
|---|---|---|---|
| Server to folder | `discord export {server?, force?}` | `dyna discord export ROOM [--server ID] [--force]` | the folder |
| Compare | `discord plan {server?, prune?}` | `dyna discord plan ROOM [--server ID] [--prune]` | nothing |
| Folder to server | `discord apply {server?, prune?}` | `dyna discord apply ROOM [--server ID] [--prune]` | Discord, and new ids into the folder |

Use the folder for structure that should last (channels, categories, roles, overwrites, standing
messages such as rules and welcome, the front policy). Use `api` calls for one-off acts and for
everything about people (membership, moderation).

## Adopt a server, then change it
1. `discord export`. It refuses a non-empty folder unless `force`; force replaces configuration
   files only, keeping messages, policy.yaml and unmanaged areas. It exports no message history
   and no role membership. Read its report: it names what it left to Discord and why.
2. `discord plan`: expect `0 changes`. Mark what scripts or other bots feed with `UNMANAGED.md`.
3. Edit files, `discord plan`, read every line, then `discord apply`. Plan again: `0 changes`.
4. To delete: remove the folder or entry and its name from any `channels:` or `categories:` list,
   `discord plan` with `prune`, check that the `Delete …` lines are exactly what you mean, then
   `discord apply` with `prune`.

## The tree
```text
discord/
  server.yaml              settings, category order, roles, AutoMod
  policy.yaml              the front policy (format in SKILL.md)
  channels/
    Community/             a category is a folder
      category.yaml
      general/             a channel is a folder
        channel.yaml       may be omitted for a plain text channel
        welcome.md         a standing message
    hchat/
      UNMANAGED.md         apply never touches this category or anything in it
```
`server.yaml`: categories in sidebar order, roles top first.
```yaml
settings: {verification: medium, notifications: mentions, community: {rules: rules, updates: announcements}}
categories: [Community]
roles:
  - {name: Moderator, permissions: [manage_messages, moderate_members], color: "#e67e22", hoist: true}
automod:
  - {name: no-invites, keywords: ["discord.gg/*"], block: true, alert: mod-log}
```
`category.yaml` gives the order of its channels (list them all, or plan keeps reporting the order)
and may carry overwrites:
```yaml
channels: [general, showcase]
deny: {everyone: [send_messages]}
```
`channel.yaml`: type is text, voice, announcement or forum.
```yaml
topic: Ask anything about the project.
slowmode: 10s
allow: {Moderator: [view_channel]}
deny: {everyone: [view_channel], "member:1234567890": [view_channel]}
```
Permissions use Discord's snake-case names. Overwrites name a role, `everyone` or `member:ID`.
Unless the bot has Administrator, a private channel must also allow the bot's own role.
Every YAML file is strict: an unknown or duplicate key, an alias or an unknown permission refuses,
naming file and line.

## Identity: id, was and UNMANAGED
- `id: "123"` ties an entry to one Discord object. Export writes it; apply writes it when it creates
  something. With an id, a rename or move on either side is matched, never duplicated.
- `was: old-name` adopts the object with that name and renames it to the entry's name. Remove the
  line before you rename the entry again. Rename a channel or category that has an id by renaming
  its folder. Never set both id and was.
- An entry with neither is new: apply creates it. If an object of that name already exists, apply
  refuses until you add its id or a `was:`; it never takes over by name.
- An adopted object that is gone from the server is a refusal, never a re-create: remove the entry,
  and add it again under a new name if you want a new object. Apply remembers the ids it recorded,
  so deleting an `id:` line does not detach an entry.
- `UNMANAGED.md` in a category or channel folder excludes it and everything under it; its first
  line is the reason shown in plan. Files left beside it are ignored. Export writes one itself for
  what the format cannot hold (stage and NSFW channels, forum defaults).
- Left on Discord and never changed: `managed: true` roles (bots, boosters), `other_permissions`
  bits, role icons, AutoMod rules outside the format above, role membership, message history.

## Standing messages
A `.md` file in a channel folder is one message the bot keeps posted there, as an embed:
```markdown
---
title: Welcome
color: "#5865f2"
footer: Updated by the team
pin: true
---
Read #rules first, then say hello in #general.

## Getting help
Ask in #{help desk}; a @Moderator will answer.
```
Text before the first heading is the description; each `## Heading` is a field. `#channel` and
`@Role` become mentions (`#{name with spaces}`); an unknown name refuses with file and line, and
nothing in a standing message pings. `embed:` in front matter (a JSON object as a string) replaces
the generated embed. Edit the file and apply to edit the message in place. The file name is the
message's identity, kept in the engine: renaming the file posts a new message.

## Reading plan and apply
- `Create …`, `Change KIND NAME FIELD: before → after.`, `Edit message … in place`, and with prune
  `Delete KIND NAME (ID …).`; then `N changes; M unchanged.`
- `X is on the server, not in the folder.`: left alone unless you add it or prune something recorded.
- Not applied, and plan may still list them as changes: a channel's type and forum tags once it
  exists and turning Community off (apply reports `By hand: …`), `sync` (ignored), and role order
  involving a managed role or one at or above the bot's top role (`Change role order by hand: …`).
- Apply prints `Done:`, `Failed:` and `Not done:` per step and stops at the first failure; run it
  again to resume. It never repeats a create blindly: after an unclear answer it retries only if
  nothing new appeared on the server, and otherwise refuses until you add the id of the object that
  is yours. A message whose post is unclear refuses until you delete any copy and rename its file.
- Apply order: roles, channels, Community, forum and announcement channels, overwrites, channel and
  role order, AutoMod, messages, prune, policy.yaml.
- `prune` deletes only objects apply created or the folder adopted and no longer lists, never
  messages, and refuses a category holding a channel it is not deleting.

## When someone changes the server by hand
Nothing reverts on its own. `discord plan` shows a changed channel, category, role or setting as a
change; `discord apply` puts it back. To keep a hand change, edit the folder to match (for a move
or rename, move or rename the folder); `discord export` with `force` rewrites files in place but
does not move or remove folders. A standing message edited or deleted by hand is not noticed:
plan compares the file with what apply last posted, never with the channel. The last 20 applied
copies of the folder are in `rooms/ROOM/discord-applied/SERVER_ID/`. `hooks.yaml` is recognized
but not applied yet.
