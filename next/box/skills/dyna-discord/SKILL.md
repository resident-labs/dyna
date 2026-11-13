---
name: dyna-discord
description: Use when a Discord message reaches you, or you relay one, set up a session to answer a server, look up channels, roles or members, change channels, permissions or roles, moderate members, or post threads, polls or embeds.
---
# Discord

One bot serves the whole machine. Each mapping of your room routes a server or one channel to an
answering session; dyna_discord lists them and the channels of whole-server mappings. The answering
session and the sessions above it get the discord tool. Its `api` action is Discord REST with the
bot's permissions, so the recipes from "Find things" on are `api` calls, and Discord's permission
rules apply on top of the mapping's scope: a `forbidden` comes from Dyna (outside your mapping), a
status 403 from Discord (the bot lacks a permission or its role is too low).

## Answer a message

- Only messages that mention the bot or its role, reply to one of its messages, or DM it arrive as
  input. Other messages, reactions and button clicks never arrive; `discord read` finds them.
- Your final text answers each channel's newest input of the turn, as a reply to it.
- A `discord send` with `replyTo` an input's message, or an `api` POST to
  `/channels/CHANNEL/messages` with `message_reference: {message_id: MESSAGE}`, is that input's
  answer: your final text then goes only to the other inputs. An answer sent without either is
  followed by your final text in the same channel, so give an answer you send yourself a `replyTo`.
- While a child of yours is at work or a job of yours runs, a turn that ends with no final text
  keeps the answer owed; the first later turn with final text answers it.
- A DM is answered by your final text only: `send` and `api` refuse DM channels. A DM reaches the
  room whose whole-server mapping has the author as a member; with none or several, the bot asks the
  author to mention it in a server channel.
- A server input carries up to 20 earlier messages of the channel (4,000 characters in all, the
  bot's own cut to 300); a DM carries none. `discord read` gets more.

## Pass a question up

Post to your parent with the channel ID and message ID from the input frame. The parent has the
tool too and answers with `discord send` and that `replyTo`. If the parent replies to you instead,
relay with `discord send` and `replyTo`: a turn without a Discord input sends nothing to Discord on
its own.

## Set up a front session (root)

On a public server everything people write is outside text, so the answering session should hold
nothing worth taking: a cheap child of root with no repository and no host grant.
1. `session start` it with a cheap profile, no repo and no goal. It has no finish line, and an idle
   session with an open goal is reported as stalled.
2. Point the mapping at it with the owner CLI (skill dyna-admin):
   `dyna room discord ROOM --server SERVER --session NAME`, plus `--channel CHANNEL` for a channel
   mapping. The mapping keeps its instructions. `dyna room discord ROOM` lists the mappings; undo
   with `--session root`. A closed answering session falls back to root.
3. Shape it with `discord instructions`: what it answers itself, what it posts to you, its tone.
   Read the current text first (no `text`): a write replaces the owner's words for that mapping too.
4. Whether a box has the discord tool is decided when it starts: `session refresh` the front, and
   your own session if your discord tool is missing.

## Find things

| What | `api` call |
|---|---|
| Server | `GET /guilds/SERVER` |
| Channels, categories | `GET /guilds/SERVER/channels` |
| Roles | `GET /guilds/SERVER/roles` |
| A member by name | `GET /guilds/SERVER/members/search?query=NAME&limit=10` |
| A member | `GET /guilds/SERVER/members/USER` |
| Any user | `GET /users/USER` |
| Bans | `GET /guilds/SERVER/bans` |
| Who changed what | `GET /guilds/SERVER/audit-logs?limit=20` |
| Messages | `discord read` (not `api`) |

IDs are strings. A channel mapping cannot call any `/guilds/…` path: it reaches its channel, the
channel's threads and `/users/USER` only.

## Channels and permissions

- Create: `POST /guilds/SERVER/channels` with `{name: "help", type: 0}` (text), `type: 4`
  (category) or `type: 15` (forum); `parent_id: CATEGORY` puts it in a category.
- Edit: `PATCH /channels/CHANNEL` with the fields to change; delete: `DELETE /channels/CHANNEL`.
- One overwrite: `PUT /channels/CHANNEL/permissions/ROLE_OR_USER` with
  `{type: 0, allow: "1024", deny: "0"}` (type 0 a role, 1 a member; bitfields are decimal strings).
  `DELETE` on that path removes it. Prefer this to a PATCH of `permission_overwrites`, which
  replaces the whole list.
- Private channel: deny VIEW_CHANNEL (1024) to the `@everyone` role, whose ID is the server ID, then
  allow it to the role that should see it, and to the bot's own role: without Administrator the
  bot loses the channel too.
- Needs MANAGE_CHANNELS, and MANAGE_ROLES for overwrites. Check by reading the channel back.

## Roles

- Create: `POST /guilds/SERVER/roles` with `{name: "Helpers", color: 3447003}`; edit with
  `PATCH /guilds/SERVER/roles/ROLE`; delete with `DELETE` on that path.
- Give: `PUT /guilds/SERVER/members/USER/roles/ROLE`; take away: `DELETE` on that path.
- Needs MANAGE_ROLES, and the bot's highest role must sit above the role it creates, edits, gives
  or takes; Administrator does not bypass that. A 403 here usually means the order: the owner moves
  the bot's role up in Server Settings.

## Moderate

Give `reason` on every moderation call. The bot's highest role must sit above the member's.
- Timeout: `PATCH /guilds/SERVER/members/USER` with
  `{communication_disabled_until: "2026-11-12T12:00:00Z"}`, at most 28 days ahead; `null` lifts it.
  Needs MODERATE_MEMBERS; admins and the server owner cannot be timed out.
- Kick: `DELETE /guilds/SERVER/members/USER` (KICK_MEMBERS).
- Ban: `PUT /guilds/SERVER/bans/USER` with `{delete_message_seconds: 0}`, up to 604800 (7 days) of
  their messages; unban with `DELETE` (BAN_MEMBERS).
- Delete a message: `DELETE /channels/CHANNEL/messages/MESSAGE` (MANAGE_MESSAGES for others').
- Purge: `POST /channels/CHANNEL/messages/bulk-delete` with `{messages: ["ID1", "ID2"]}`: 2 to 100
  IDs, none older than 14 days, or the whole call fails; delete older ones one by one.

## Threads, forums and pins

- Thread on a message: `POST /channels/CHANNEL/messages/MESSAGE/threads` with
  `{name: "Follow-up", auto_archive_duration: 1440}`; the thread's ID is the message's ID.
- Forum post: `POST /channels/FORUM/threads` with `{name: "Topic", message: {content: "Text"}}`,
  plus `applied_tags: ["TAG"]` when the forum requires tags.
- Pin: `PUT /channels/CHANNEL/messages/pins/MESSAGE`; unpin with `DELETE` (PIN_MESSAGES).
- Threads of a mapped channel are in scope; to post in one, use the thread's ID as the channel.

## Rich messages

- Your final text and `discord send` split at 2,000 characters, keep code fences whole, and ping
  users only, never roles or @everyone. An `api` POST pings whatever its content mentions unless it
  sets `allowed_mentions`, for example `{parse: ["users"]}`.
- Mentions: `<@USER>`, `<@&ROLE>`, `<#CHANNEL>`.
- Embed: `POST /channels/CHANNEL/messages` with
  `{embeds: [{title: "Status", description: "Details", color: 3447003}]}`.
- Poll: the same POST with this body; `duration` is in hours, up to 32 days.
  `{poll: {question: {text: "Ready?"}, answers: [{poll_media: {text: "Yes"}}, {poll_media: {text: "No"}}], duration: 24}}`
  Voters of one answer (its `answer_id` from the poll message):
  `GET /channels/CHANNEL/polls/MESSAGE/answers/ANSWER`. End it early:
  `POST /channels/CHANNEL/polls/MESSAGE/expire`.
- Buttons: only link buttons do anything, because clicks never reach Dyna:
  `components: [{type: 1, components: [{type: 2, style: 5, label: "Open", url: "https://…"}]}]`.
- Edit a bot message: `PATCH /channels/CHANNEL/messages/MESSAGE`.

Reference: https://discord.com/developers/docs/resources/message and
https://discord.com/developers/docs/topics/permissions
