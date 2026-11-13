# A server's private folder

The engine reads `rooms/ROOM/discord/` beside the room's AGENTS.md on the engine host.
Never put this folder in a repository or copy it into a box. With several whole-server mappings,
use `discord/SERVER_ID/` for each server. Only the room's root and owner can export or plan.
Owner CLI: `dyna discord export|plan|apply ROOM --server ID`. With one mapped server, omit server.
Tool calls use `discord export {server?}`, `discord plan {server?}` and `discord apply {server?, prune?}`.

Export first to adopt an existing server. It refuses a non-empty folder unless `force: true`
(CLI `--force`); force replaces configuration only, preserving messages, policy and unmanaged areas.
It exports no message history and never exports or applies role membership. Plan is read-only.
`hooks.yaml` is recognized but not applied.

```text
discord/
  server.yaml
  policy.yaml
  channels/
    Community/
      category.yaml
      general/
        channel.yaml
        welcome.md
    hchat/
      UNMANAGED.md
```

`server.yaml` holds settings, categories in sidebar order, roles top first and AutoMod rules:
```yaml
settings: {verification: medium, notifications: mentions}
categories: [Community]
roles:
  - {name: Moderator, permissions: [manage_messages, moderate_members], color: "#e67e22", hoist: true}
automod:
  - {name: no-invites, keywords: ["discord.gg/*"], block: true, alert: general}
```
Community settings are `community: {rules: rules, updates: announcements}` inside settings.
Names must resolve uniquely. Role permissions use Discord's snake-case names.
Export marks channels outside this schema with generated UNMANAGED.md reasons and reports them.
Role icons/emoji remain Discord-owned. Exported `managed: true` roles are read-only; opaque
`other_permissions` bits are preserved, never removed by a plan. Unsupported AutoMod rules stay
on Discord and are reported, not adopted. Unsafe names, duplicate siblings and nested categories refuse.

`channels/Community/category.yaml` controls category overwrites and its children:
```yaml
sync: false
channels: [general]
```
A channel without a category lives directly under channels. `channel.yaml` may be omitted for plain text.
```yaml
type: forum
tags: [question, bug]
slowmode: 10s
allow: {Moderator: [view_channel]}
deny: {everyone: [view_channel], "member:1234567890": [view_channel]}
```
Channel types are text, voice, announcement and forum; topic is a string.
Overwrites use role names, everyone, or member:ID; categories use the same allow/deny syntax.
An `id: "123"` adopts an object; `was: old-name` adopts and renames it. Never use both.
Missing adopted objects refuse rather than becoming replacements. Rename the folder to rename a channel/category.

A category or channel containing `UNMANAGED.md` is excluded, along with all descendants.
Its first line appears in the plan as the reason. Use this for external webhook or service-owned channels.
The engine does not inspect descendant configuration there or write over it, even with force.

Standing messages are `.md` files in a channel folder. Front matter accepts title, color, footer, pin,
mentions: none, and embed (a string containing a JSON object). The remaining Markdown is the body;
`## Heading` describes an embed field. `#channel` and `@Role` are references for future apply.
Message names are stable ownership keys; do not rename a file to edit its message.

`policy.yaml` uses the front-policy format in SKILL.md, not the server settings format.
All YAML files are strict: unknown/duplicate keys, aliases and unknown permissions refuse.
Do not add unsupported fields or role membership. Keep policy and private channel configuration on the host.

## Apply

Run plan first, then `discord apply`. Apply owns only what it created or the folder adopts by `id:` or
`was:`; an object matched by name alone is refused until you add its `id:`. When apply creates an object it
writes the new `id:` into the file, so a hand move or rename on the server is later matched by ID.
It never touches UNMANAGED folders, Discord-managed roles, `other_permissions` bits, role membership or
message history, and a missing adopted object is a refusal, never a re-create.
Order: roles, channels (Community's channels, then Community, then forum and announcement), overwrites,
channel and role order, AutoMod, messages, policy.yaml. Apply twice changes nothing the second time.
A failure stops and lists what was done and what remains; run apply again to resume. After an unclear
answer, nothing new on the server means the create did not happen; anything new refuses until you add
`id:` to the entry that is yours (never a guess by name). A standing message whose post may have
reached Discord refuses until you delete any copy and rename its file.
Run `plan` with `prune` before `apply` with `prune`: it lists each object that would be deleted, or the refusal.
`prune` deletes only recorded objects the folder no longer lists, never messages, and refuses a category
holding a channel apply does not own. By hand (reported, not applied): channel type and forum tags of an
existing channel, turning Community off, `sync` (derived from the channels' overwrites), role order above the
bot. List every channel of a category in `channels:` or plan keeps reporting its order.
The last 20 applied copies are kept beside the folder in `discord-applied/SERVER_ID/`.
