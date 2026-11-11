# Dyna v2 specification

This is the current design for `next/`. Spec changes land in reviewed PRs, and only the owner can change a settled owner decision. [decisions.md](decisions.md) records the decisions made since the v2 rewrite and where the rewrite's full history is.

## 0. Settled decisions

Only the owner can reopen a settled owner decision.

1. **Events.** Posts and system events are SQLite rows. Transcripts and large outputs stay files.
2. **Box budgets.** Each box has its own top-level slice and hard size limit. Admission counts a smaller request, and the engine accounts for the room limit. The session tree carries authority, close order and routing, not resources. The owner accepts slowdown under pressure, not kills of healthy boxes. A build can exit 137 if RAM and swap fill; its box and session stay.
3. **Model credentials.** CLIProxyAPI holds and refreshes model credentials. Engine code and PiG do not read them; the engine adds only the proxy client key. The proxy stores tokens in its own table in `store.db`. The Accounts page drives proxy sign-in and quota reads.
4. **PiG only.** A session runs on PiG (github.com/MichaelKinsy/PiG), the Go port of Pi, embedded in the box agent's own process. Dyna pins an exact commit of `resident-labs/PiG`'s `dyna` branch, which is a PiG release plus small additive patches recorded in its `PATCHES.md`. There is no Claude Code fallback adapter. Claude uses Pi's `anthropic` provider through CLIProxyAPI's Claude Code cloaking; ChatGPT uses `openai-responses`. The owner accepts that cloaking may flag an account or cause extra billing. An Anthropic request that marks effort changes mid-conversation still sends the session's effort at the top level, where upstream sends `high` (the fork's patch P8).
5. **Dormancy.** A dormant session stops its container, keeps its volume, and no longer counts its host request. Wake starts it.
6. **Host exec.** Only the owner grants it. Root's default grant covers all hosts, including the control plane, without approval. Root can therefore reach model sign-ins and the GitHub token. This is the exception to the no-secret-in-a-box goal. A control-plane grant gives full owner power, including owner-only API calls (owner, 2026-10-31, msg_462f3f1d210856c2).
7. **Root.** Root is a position in the tree. An ancestor can start, move or close descendants. Root's extra powers are the ask-owner and host-exec grants and the engine-notice subscription.
8. **Self-hosting.** Dyna develops itself in its own room. v2 replaced v1 at the cutover; v1's last source is tag `v1-final`.
9. **Scope.** Voice is not ported yet. The library is cut for now; the frozen v1 backup keeps its feature map.
10. **Node jobs.** Do not port them. GitHub Actions runs CI, with Dyna's self-hosted runners of the size each job asks for, placed as boxes are on hosts with ci on (the default, except on the engine's own host) ([CI runners](ci-runners.md)). Host-specific work uses host exec or a room restricted to a suitable host. Admission does not yet select hosts by device needs.
11. **Proxy process.** Vendor CLIProxyAPI in `next/third_party/cliproxyapi`. The engine supervises a child of the same binary over a Unix socket.
12. **Cuts.** Keep subtree moves, `Run` subscriptions and `show_pty` requests. Defer box growth, cross-host restart, checkpoints, relocation commands, bundle data for issue watches and room MCP servers, one cgroup per shell job, `node_modules` reflink seeding and external members. Ship `box.pressure`; use process groups for shell jobs and a manual relocation procedure. A restart of a box whose creation failed may be placed on another host, because a failed creation has no volume to relocate; deferring the relocation of existing boxes still stands.
13. **GitHub.** GitHub is the source of truth for PRs, merges and CI. A room may select its own GitHub account and commit identity through the owner-only `dyna room github ROOM --name NAME --email EMAIL` command, with its token on stdin; `--clear` restores the engine default and no flags shows only public metadata. The engine validates `/user` before saving. Account rooms never fall back to the engine token. Git, mirrors, MCP reads and writes, Runs, PR creation, feeds and tracked PR/issue reads select that account; per-account clients keep independent quotas and viewer identities. Repository-scoped work uses `repos.default_room`, and attaching across different accounts is refused. Setting, clearing or changing to a different account login requires detaching all of the room's repositories first; replacing the token for the same login stays allowed. Account-backed mirror and host-cache reads resolve credentials and check repository access before use, including recent-fetch reuse and compose reads. Missing credentials or GitHub 401, 403 or 404 fail closed, and an account fetch failure does not seed from an old mirror. Room-account mirrors, and any mirror previously discarded on an identity change, record the filling login and a refill generation in a small marker file. Ordinary engine-account mirrors stay unmarked after fetches, preserving existing capacity-host caches and offline setup behaviour. A different current login (or no marker for a room account) discards the old mirror and requires a successful fresh fetch, even for an offline setup or compose read; a durable pending marker prevents a failed refill from appearing to be a legacy engine mirror. Unmarked engine-account mirrors retain their existing offline behaviour. Capacity-host caches compare the mirror marker; whenever identity or refill generation changes, the old cache is moved aside, the new cache is initialized, and only then is the old folder removed before receiving new objects. An unmarked engine mirror and unmarked host cache continue to match without a reset. CI cannot be enabled for an account repository. Credentials live in private 0600 control-plane files, never the store, boxes, events or API responses; SQLite holds public identity and an opaque credential file reference. Dyna stores PR ownership and the approved head, not review records. The owner, an ancestor of the author, or another child of that ancestor may approve; the author and its children may not. A new head clears approval. Dyna merges only with green CI, passing the approved head SHA so GitHub rejects a moved head. It uses GitHub's merge queue where configured. Self-hosted runner boxes get single-job just-in-time tokens; outside PRs use GitHub-hosted runners or wait for the owner. Room policy chooses approvers. Drop comment trust, merge trials, CI time budgets, conflict radar and room PR views.
   - Text that reaches an agent through Dyna from outside the room sits in one wrapper, `contract.Outside(source, author, text)`: `<outside source="github" author="@octocat">`, the text, `</outside>`. The source says where the text came from (`github`, `web`, `subscription <id>`, `mcp <server>`) and the author, when known, who wrote it; an empty author leaves the attribute out. The engine turns `<outside` and `</outside` inside the text, in any letter case, into `‹outside` and `‹/outside`, so the text cannot close the block or fake another. The wrapper carries no warning: the box's startup prompt says once what it names. It covers GitHub text from anyone other than the owner's account in notices and in `pr status` and `issue status`, a subscription Run's output, each web search result's title and snippet (author: the result's domain), and the tool results of the `github` MCP server and of other MCP servers, which the box wraps with the same function (author unknown, so omitted); the `dyna` server's own tools are not wrapped. The box wraps a model's direct calls only: a codemode script reads the raw result of its own calls, and its output reaches the model as it wrote it. The tag is escaped also with spaces or zero-width characters after `<` or `/`, and `"`, `<`, `>` and line breaks are dropped from source and author. An MCP server name has letters, digits, `-` and `_`, with no `_` at either end and no `__`, so a tool name `mcp__<server>__<tool>` names one server. The owner accepts that this does not stop prompt injection.
   - Agents use the real `gh` through the engine without a token in the box. Box calls share the quota, and direct merge calls are refused in favor of `dyna pr approve`. The engine records new PR ownership. The owner accepts that other writes have no approval or repository-scope check, responses are not wrapped, and other APIs can still move a base branch.
14. **Always-on engine.** The control plane runs on an always-on host, which may also host boxes. The web listener binds loopback; SSH forwarding is used until paired remote access. The owner CLI uses the forwarded owner socket or runs on the host. The GitHub token remains in the host's `gh` store. A reboot restarts the engine and boxes restart on wake.
15. **Agent surface.** The box agent serves Dyna operations through MCP. PiG's context update puts the snapshot in the system prompt on first run and compaction, and sends changes to sections, tools and skills as one user-turn update on every provider. The CLI remains. Section 7.9 specifies the mechanism and phase plan.
16. **Tests.** Use one or two integration tests per important owner-facing feature and no repository end-to-end tests. v1's room history was not carried over, and no archive reader is planned.

## 1. Goals and non-goals

### Goals

1. **Agents never load the owner's machine.** Every session runs in a box on a capacity host. The control plane runs no agents, builds or tests.
2. **One contract.** Events, operations, grants, the API and the migration bundle are defined once in `contract/`. The engine, the CLI, the box agent and the web app use the same types. TypeScript is generated.
3. **No secret in a box, with one exception by owner decision.** Model calls, git, GitHub writes and host commands go through the engine. The engine knows the caller by its socket. The exception is root: its default host exec includes the control plane, which holds the model sign-ins and the GitHub token (settled decision 1).
4. **Small and legible.** Under 60,000 Go lines without tests, in nine top-level packages, with no compatibility code. v1 had about 260,000 in 138 packages.
5. **Fast, honest tests.** The repository holds fast unit tests, plus one or two fast integration tests for each important owner-facing feature. Real harness, GitHub and browser behaviour is checked by hand (10.1).
6. **A clean cutover (done).** Rooms were rebuilt by hand, with no import and no rollback. The store guard (8.4) protects every other install.
7. **Useful to any owner (r9).** Any repository and toolchain (images saved in the room), any Linux host with rootless Podman and SSH, and every model account that CLIProxyAPI supports.

### Non-goals

- No v1 compatibility in `next/`: no v1 store reader, no v1 wire fields, no legacy shims, no rollback path.
- No harness other than PiG. Claude Code is not ported (section 0, decision 4).
- No agent work on the control plane: no host worktrees, host shells, host browsers, room slices or test-slot wrapper.
- No root seat and no attach or detach of outside CLIs.
- No shell hooks. Hooks are typed event subscriptions.
- No JSON state files. JSON stays for settings and config.
- No new web framework. The Svelte app has the quiet, dense look of 5.13.
- No shared or untrusted hosts. Boxes run only where only we have admin.
- No end-to-end tests in the repository, and only one or two fast integration tests per important owner-facing feature (10.1).

## 2. Layout

```text
next/                    module github.com/resident-labs/dyna (go 1.26), no go.work
  go.mod
  cmd/dyna/              main: the one binary
  contract/              shared types; imports only the standard library
  engine/                the control plane: store, API, approvals, model route, git, timers
  box/                   hosts, images, budgets, boxes, and the box agent
  harness/               the PiG adapter and the fake harness
  room/                  pure room rules: tree, addresses, delivery, reply channels, wake
  web/                   the ported Svelte app; types generated from contract/
  docs/                  this spec and the guides
  third_party/cliproxyapi/  vendored CLIProxyAPI fork: its own Go module, our patches, DYNA.md
```

Main has no v1 tree. The old module (`dyna-agent`) and `cmd/dyna-export` exist only at tag `v1-final`.

**One binary, three roles.**

| Command | Where it runs | What it does |
|---|---|---|
| `dyna engine` | control plane | serves the API, owns the store, supervises CLIProxyAPI and runs the engine timers |
| `dyna box-agent` | in each box | turn runner, shell jobs behind the `bash` tool, the `dyna` MCP server (7.9), transcript stream, model and git routes into the box |
| `dyna <noun> <verb>` | anywhere | the CLI. In a box it uses the box socket. On the control plane it uses the owner socket. On the owner's desktop it uses the owner socket through an SSH forward (`--socket`, decision 14). |

**Dependency direction.** `contract` imports nothing from the module. `room` imports only `contract`, and it does no I/O: it decides, and the engine acts. `harness` imports only `contract`. `box` imports `contract` and `harness`, because the box agent runs the adapters. `engine` imports `room`, `box`, `harness` and `contract`. `engine/runtime` also imports `web`, which only embeds the built assets. `cmd/dyna` wires them. `engine/store` imports `contract`, `room` and `box`, and nothing else from the module. A unit test enforces this with `go list -deps`.

Sub-packages are the builder's choice. The expected ones are `engine/store`, `engine/api`, `engine/approve`, `engine/model`, `engine/git`, `engine/sched`, `engine/lifecycle`, `box/podman`, `box/fakehost`, `harness/pig` and `harness/fakeharness`.

`engine/lifecycle` is the box reconciler (5.3). It holds the reconcile loop, box admission (5.5) and the box steps. It imports `engine/store`, `box` and `contract`. It reads the engine only through a small interface: `OpenTerminal(session)`, `Connected(session)` and `Stale(session)`. `engine` imports `engine/lifecycle`, never the reverse. API methods in `engine` write box rows and call `Kick()`; they do no host work.

Third-party Go dependencies are allowed only with a reason in the PR. Expected: `modernc.org/sqlite` (pure Go, as in v1), `github.com/spf13/cobra` for the CLI, and the vendored CLIProxyAPI with the modules it needs (5.7). The web app keeps Svelte 5, Vite and strict TypeScript (r7).

## 3. Contract

### 3.1 Rules

`contract/` owns types crossing process boundaries or stored as typed bodies. It uses named closed sets and validates unknown values once at the API boundary. Times use `time.Time`; durations use integer milliseconds. `go generate ./contract` generates union codecs and the TypeScript client; CI checks freshness. Within v2 the API is additive for release skew.

### 3.2 Identifiers and principals

Room and session names contain 1–64 ASCII letters, digits, dots, underscores or hyphens, starting with a letter or digit. Contract types distinguish room, session, box, event, operation, grant, turn, device, subscription, goal, host, repository and profile identifiers. The engine derives the principal from the connection, not the request body. Principal kinds are owner, session and engine. Root is the session without a parent, not a special principal.

### 3.3 Event

An event is one stored fact, with a sequence, ID, time, room, principal and registered typed body. One generated discriminated union covers each body. Wake is a delivery decision, not an event property. Typed failure causes and notice kinds drive owner-facing views.

`turn.activity` is a live, unstored event with sequence 0. The box reports its current tool call at most once a second. Each carries a closed `kind`, `tool`, `compaction` or `model`, set by the harness beside the label, and `SessionSummary.activityKind` carries the kind of `activity`. A report with no kind, from a box agent that predates kinds, reads as `tool`; any other kind is invalid. The engine streams it and clears it when the turn ends. It does not change a resume position.

### 3.4 Delivery and reply channels

The engine stores an event once and creates one delivery per recipient. A delivery records its source surface, reply channel and wake decision, and, when it went up past a parent that could not receive, that parent and why. The pure `room` package computes the mapping in section 7.5, and the store's `receiver` applies the chain of command. Final replies target a principal and conversation; an already-posted response is delivered quietly with a link. A turn can owe replies to more than one distinct target and home.

### 3.5 Operation and grant

Agent writes that need a decision are typed operations. The engine registry supplies the class, card, check and executor. The owner's own operations and `auto` operations run at once; `owner` operations wait for a decision. Host access follows holders in the tree (6); other kinds wait for the owner unless a room setting permits root. A kind may also let the room's root decide it when a room setting allows. Each setting is a `RootApproval` kind in `rooms.root_approves`, a JSON set that is empty by default: `addons` `addon.global`, `hosts` `room.hosts`, and `pools` `pool.add`. Only the owner changes them, with `room.set` (`rootApproves`, kind to on or off; an omitted kind stays) or `dyna room set <room> --root-approves-KIND on|off`, and `room.list` shows the set. The room's root sees the kinds that are on in its `dyna_room` section (7.9), so a switch the owner flips reaches it as a section change at its next turn; no other session has the section and no notice is sent. With a kind off the operation waits for the owner as before; with it on the root decides it with `op decide` as the owner can, including its own request, and the owner can still decide. The request still waits for a decision, and its card says who can approve. No setting covers CI switches (`repo.ci.set`, `host set --ci`), which are owner-only calls with no request to decide, or merging, which Dyna does not do (7.6). The engine rechecks before execution and fails stale cards without running. Pending operations expire after 24 hours by default, except `hostexec.grant`, which waits until it is decided or its session closes, across engine restarts; identical pending requests from the same caller return the existing operation. Waiting, decision and execution emit typed events. The request event records the decider at request time; pending operation and room lists resolve the current decider.

Grants record powers held by sessions in the tree. A new room grants root `ask-owner` and `hostexec` on all hosts, with no expiry, and subscribes it to notices. Host exec requests climb to the nearest ancestor holding every requested host, otherwise the owner. Section 6 describes the rules.

### 3.7 API

HTTP/1.1 serves typed `POST /v1/<method>` calls and typed errors. The owner Unix socket has mode 0600. Each box has its own Unix socket. The web listener binds loopback; paired devices use remote access. The listener identifies the principal and applies device scopes. Event streams use server-sent events and resume by sequence. The owner stream spans rooms; box traffic uses a persistent channel.

Owner-only `session.work` returns a newest-first page of turns, each with its start, optional end, input events and posts sent during that turn. The cursor is a turn ID. The default page has 30 turns and the maximum is 100.

Visual cards use stable IDs and versioned posts. Only the author or owner updates a card, within its room. The server checks file access and serves the HTML in a sandbox with a default-deny content security policy; interactive submissions require owner confirmation.

Owner-only search covers sessions and posts. Owner-only shell watch and terminal routes expose box output and terminals. Owner-only `box.stats` reads a running box's CPU, memory and disk while a panel shows it: the engine asks over the box channel, and the agent answers from its cgroup and a `du` cached for five minutes. Limits come from the box's size, because a box cannot see its slice. Nothing polls boxes in the background, and no reading is stored. The owner advances conversation read positions; room and session lists provide their current read and activity state. Session stop and refresh are available to the owner or an ancestor, and a session can refresh itself. Owner or room root can pause delivery and box wakes while active turns finish; resume dispatches waiting wakes. Standing-instruction reads and writes are owner-only, with hash-based stale-write protection (section 7).

The contract package is the authority for method names, fields, body variants and generated client types.

## 4. Store

### 4.1 Rules

The engine holds one SQLite `store.db`, protected by an exclusive `store.lock`. It uses WAL, `synchronous=NORMAL`, foreign keys and incremental auto-vacuum. `schema.sql` is the current schema; fresh stores run it, while existing stores run numbered steps from their version. The engine refuses a live schema that differs from it. Stores below the upgrade floor get `store_too_old` and the commit to update through (r17). The guard refuses a v1 `dyna.db` without `store.db`, and a store without the v2 format marker.

One writer commits each state change and its events in one transaction, then publishes events in sequence order. Reads use a read-only pool. A separate connection checkpoints the WAL; commits never wait for a checkpoint. Typed body columns hold JSON, but queried state is not kept in JSON files. Settings and other configuration may use JSON. Transcripts are append-only JSONL; attachments and large outputs are files addressed by SHA-256, per room (4.3).

The engine keeps no backups: no daily copies, no copy before an upgrade and no off-host archive. Relocation uses a fresh snapshot after writes stop (5.10). After cutover, measured growth may trigger archival of events older than 180 days for closed sessions.

### 4.2 Tables

Owner-home indexes: `turns_session_ended(session, ended_at)` finds open turns; `deliveries_by_turn(to_kind, to_id, turn, seq)` finds their deliveries. The latter adds persistent storage and delivery write cost.

This table lists the current tables in `engine/store/schema.sql`, the authority for columns and indexes. Planned tables are not listed.

| Table |
|---|
| `meta` |
| `rooms` |
| `room_github_accounts` |
| `discord_routes` |
| `discord_outbox` |
| `discord_messages` |
| `repos` |
| `repo_heads` |
| `room_repos` |
| `sessions` |
| `events` |
| `deliveries` | Recipient/sequence primary key; `deliveries_open` indexes open inputs, and `deliveries_by_turn(to_kind,to_id,turn,seq)` recovers all reply channels for a turn in sequence order, including read inputs. |
| `turns` |
| `hosts` |
| `disk_warnings` |
| `pools` |
| `room_shared` |
| `host_rooms` |
| `host_labels` |
| `boxes` |
| `operations` |
| `client_replays` |
| `transcript_offsets` |
| `transcript_turns` |
| `shell_jobs` |
| `replies_owed` |
| `event_files` |
| `visuals` |
| `room_leftovers` |
| `session_branches` |
| `pr_owners` |
| `pr_stack_layers` |
| `issue_owners` |
| `github_feeds` |
| `goals` |
| `goal_items` |
| `goal_journal` |
| `checkins` |
| `grants` |
| `questions` |
| `hostexec_runs` |
| `conversation_reads` |
| `connect_attempt` |
| `connect_connector` |
| `connect_link_state` |
| `connect_nonces` |
| `connect_revocations` |
| `connect_devices` |
| `connect_sessions` |
| `devices` |
| `device_scopes` |
| `pairing_codes` |
| `accounts` |
| `account_rooms` |
| `account_alerts` |
| `session_accounts` | the account the owner chose for a session; no row means Automatic |
| `search_keys` |
| `local_usage` | what the owner's local harnesses used, by model (5.7) |
| `proxy_auths` |
| `subscriptions` |
| `subscription_events` |
| `subscription_where` |
| `subscription_queue` |
| `subscription_args` |
| `subscription_files` |
| `subscription_runs` | one row for each Run in progress, which is what stops a subscription overlapping itself |
| `workflow_runs` | one row for each run of a script, tied to its crew session (9.2) |
| `workflow_steps` | a run's journal: each step's start and result, by step key |
| `workflow_scripts` | every version of a run's script, which a reload adds (9.2) |
| `workflow_settings` | the owner's workflow settings (9.2): one row for the global default (room `''`) and one for each room that overrides it; the `settings` column holds the JSON of the values that scope sets, so a value it leaves out is inherited |
| `services` | a service's declaration and observed state (5.18) |
| `service_secrets` | the owner's secret values for a service, write-only (5.18) |
| `service_leftovers` | a removed service that a host offline at the time still holds, removed when the host returns (5.18) |
| `service_image_sizes` | the latest measured size of a service image source, separate from desired declarations and reservations (5.18) |

### 4.3 Data on disk

`box.Layout` names every path of Dyna's data on both sides. No other code spells them, and a test refuses a data folder joined anywhere else. A room's data is one folder on each side, so deleting a room removes one folder per machine.

```text
<data>/rooms/<room>/AGENTS.md                   room instructions, the store copy (the one to edit)
<data>/rooms/<room>/compose/<name>.yaml         room compose files
<data>/rooms/<room>/addons.json                 the room's skills, Pi packages and MCP servers
<data>/rooms/<room>/skills/<name>/              the room's skill folders
<data>/rooms/<room>/files/<sha256>              attachments and large outputs
<data>/rooms/<room>/transcripts/<session>/      transcript copies
<data>/rooms/<room>/shared/                     /box/shared, the one copy
<hostdata>/rooms/<room>/instructions/AGENTS.md  the host copy of the room instructions, which a box mounts
<hostdata>/rooms/<room>/instructions/addons.tgz the room's add-ons, one file a box unpacks at its start
```

`<data>` is the store and `<hostdata>` is a host's own data folder. On the control-plane host that is also a capacity host the two are different folders (`dyna-next` and `dyna-next-host` on hermeslab). The engine writes the `instructions/` files of `<hostdata>` from the store copies on every host, that one included, so the host copy is the engine's synced copy of the store copy, and an edit of the host copy alone is overwritten at the next sync. A room's `shared/` exists only in the store, at `<data>/rooms/<room>/shared/`: the engine host's boxes bind it there, and no current code creates `<hostdata>/rooms/<room>/shared/` on any host. An empty folder of that name is left over from before the shared folder moved to one central copy (commit 7d360adc0), when each host kept its own, and it holds nothing a box reads. `docs/legacy-removal.md` lists it for deletion.

Everything else belongs to no single room:

```text
<data>/AGENTS.md                                global instructions
<data>/compose/<name>.yaml                      global compose files
<data>/addons.json, <data>/skills/<name>/       global add-ons
<data>/store.db, <data>/store.lock              the store and its lock
<data>/connect/                                 Connect credentials
<data>/pools/<name>/script                      a node pool's script, which runs here and no box can reach
<data>/cliproxy/, <data>/cliproxy/auth-v2/      the model proxy and its sign-in folder
<data>/git/<owner>/<repo>.git                   repository mirrors
<hostdata>/instructions/global/AGENTS.md        the global copy a box mounts
<hostdata>/instructions/global/addons.tgz       the global add-ons, as the room's
<hostdata>/git/<owner>/<repo>.git               repository caches
<hostdata>/bin/<version>/dyna                   engine binaries, with a current link
<hostdata>/cache/                               dependency caches
<hostdata>/share/rooms/<room>/shared/           /box/shared on any other host, through its one mount
<hostdata>/share/cache/                         that mount's local cache
<hostdata>/sock/share.sock                      the forwarded socket that mount reads through
<hostdata>/sock/<box>/engine.sock               a box's forwarded socket
~/.config/containers/systemd/dyna-svc-<id>.container   a service's quadlet file on its host (5.18)
~/.config/systemd/user/dyna-svc.slice           the services slice, CPUWeight=1000 (5.18)
```

Mirrors and caches are named by repository, and rooms share them, so deleting a room leaves them. Dependency caches are shared across a host. A box's socket goes with its box. Box volumes are Podman's.

Blobs are per room. A blob's name is its hash and its bytes never change, so each room that uses one holds its own hard link, with no reference counts: removing a room's folder cannot break another room. Every upload names a room. A session's room comes from its socket and the owner gives one in the query. A blob in the folder is what lets a post use it, so no table records uploads. When the owner forwards a post, the engine links each blob into the target room before the post commits. Sessions cannot reach another room's blobs. `event_files` joined to `events` gives the room of every stored reference: the owner reads a hash through its newest reference in a room that is not being deleted.

## 5. Machines and box lifecycle

### 5.1 Control plane and hosts

The control plane runs on an always-on host. That machine may also be a capacity host, reached over SSH like any other host (decision 14). The engine runs no boxes. One Dyna-owned SSH ControlMaster per host carries the engine's framed requests, and each host run has a connection of its own (6.3); a missing or invalid reply trailer is a transport failure.

Hosts come from two places: the owner registers one by hand, and a node pool (5.17) brings one when a box needs it. The owner registers hosts with `dyna host add <name> --ssh user@addr`, optionally restricting rooms; `dyna host limit <name>` (`host.set_limits`) later replaces that list, like `accounts.set_limits` for an account, and boxes already on the host stay there. A host also has two switches, which `dyna host add` takes and `dyna host set <name> --agents on|off --ci on|off` (`host.set`) turns, each on its own: `agents`, on by default, lets new session boxes land there, and `ci`, on by default except on the host the engine runs on (the host it identified as its own, 5.6), lets the CI pool start runners there. Hosts added before this default keep their switch, since the store cannot tell one never set from one set off. An owner's `host.set`, from the web or CLI, records `host.switches_changed` in every existing room's log in the same transaction as the switch update: the host name, only the switches whose stored values changed (their new on or off values), and the owner principal in the event's `from`. This audit does not wake sessions or ask for approval. A call that leaves both switches unchanged, including clearing only a create problem, writes no switch event. The event contains no SSH target, credential or token. The room list says which rooms a host serves while agents are on. With agents off, admission places no box there, and a stopped box there waits with `room-host` until agents are on again, as for a room the list leaves out. With ci off, the pool starts no runner there, and those it has finish their jobs; runners return when ci is on. Both off disables the host and keeps it registered, as before. A host added exec only (owner, 2026-11-03, for the owner's own machine) is a separate stored flag, set only by `dyna host add NAME --ssh user@addr --agents off --ci off`, and a host with both switches off that was not added so stays a full host. Nothing ever places a box or runner there, and only host exec (6.3) uses it. Its check needs SSH and bash and nothing else: no Podman, FUSE, images, cgroups, disk floor or binary on the host, and no engine sync of instructions, forwards, maintenance or image builds. Its heartbeat runs one small script over the existing SSH master each 10 s beat, with no Podman or cgroup: it reads the whole machine's cores (`nproc`), busy time (`/proc/stat`), memory in use (`MemTotal` less `MemAvailable`) and the home folder's filesystem (plain `df`, without the btrfs metadata adjustment a Dyna host's disk gets). A host where the script prints none of them is online or offline and nothing else. The engine keeps these as the host's CPU, memory and disk figures and minute history, the same fields as any host, but only for display: they are never stored with the host's disk columns, it never counts as low on disk even at 99% used, and the low-disk notice to room roots, disk eviction and placement never consider it. An unreachable exec-only host is Unreachable with its last figures dimmed and when it was last seen, as any host is, never zeros. `dyna host list` shows `online` or `offline` with `-` for both disk columns, never `low`, and `exec only` in place of the two switches; the web machine card shows the same CPU, Memory and Disk tiles as the other machines, "Online" where they show a workspace count (it runs none, so it has no count), and a CI runner line only when it has runners; with no figures yet it shows its name and Online. Turning agents or ci on with `dyna host set` runs the full check first and refuses with its reason when it fails, so the host stays exec only; success ends exec only; `--owner-only-admin` gives the same confirmation as at `host add`, which an exec-only add does not need. What runs there when a switch goes off stays until it stops. `dyna host list` shows both switches. `dyna host remove <name>` refuses while a box or a CI runner is on the host, a subscription Run is on it or a room's rule names it, and says which; otherwise it removes the row and the engine's connection and forwards. With `--discard-boxes`, boxes on the host no longer refuse it: they fail as on a host lost for good (5.10). A host that is not exec only becomes ready after checks for delegated cgroup v2 controllers, rootless Podman 5 or later, SSH StreamLocalForwarding, curl, git, FUSE with `user_allow_other` on any host but the engine's (5.6), free disk and memory, and owner-only administration. Podman is required: the installer installs it and registration reports a typed error when it is absent (owner, q60). Schedulable memory excludes the host floor; CPUs exclude one, with a minimum of one. **Service host check.** A host that runs services also needs, and `host.list` reports as a `service_host` problem without making the host unready: linger for the Dyna user, the quadlet generator (`/usr/libexec/podman/quadlet`), `pasta`, and the value of `net.ipv4.ip_unprivileged_port_start`, which sets the lowest host port a service may publish (5.18). `host.list` gains `services`: each service on the host with its room, name, state, `cpuMilli` and `memBytes`. `dyna host remove` refuses while a service is on the host, as it does for a box.

Admission serializes placement. A CI runner skips a host where a session box is pending for CPU or memory and would be placed there if the host's runners were gone: its room's rule allows the host and it fits, so only runners keep it out. A box that fits nowhere, that its rule keeps off the host, or that other boxes keep out holds no runner back. Each placed box counts one quarter of its CPU and memory size until stopped, failed or deleted; lost boxes count. A CI runner is busy for its whole job but uses about half its CPUs on average, so the runners on a host also fit in twice its CPUs at their full size. Runner memory limits fit in the host's schedulable memory at 1x, since a compile can peak near its limit. A CI runner prefers a host that already has the current runner image over one with more free memory, and goes to a host that must build it only when no host with the image has room; the pool builds the image on every ci host in the background, outside its lock, so a new host or a deploy that changes the image delays no job that another host can take. A stopped box counts only disk. A host must fit requests, remain below 80% measured memory and retain its disk floor after new requests. A room that no host serves, because each is limited to other rooms or has agents off, waits with `room-host`; other waits report their reason. `--git.name` and `--git.email` set box Git identity only when nonempty (owner, q61). A service counts as a pending box for this rule: a service that is pending for CPU or memory and would be placed on the host if its runners were gone keeps runners off it until it is placed (5.18).

**CI pool lease.** The pool needs a nonempty GitHub credential from the engine's startup `GH_TOKEN`. Without one, the scale-set boundary creates no client, the engine stays up and the pool stays off, logging `CI pool off: no GitHub credential` once. A nonempty credential goes unchanged to the personal-access-token client; GitHub rejects an invalid credential through the normal API error path, not a local token-format check. The repository variable `DYNA_CI_POOL_UNTIL` is the lease that routes a run's jobs to Dyna's runners ([CI runners](ci-runners.md)). The engine holds it for a repository whenever the pool is alive for it, which needs both: (1) a scale-set listener of the repository is running and has polled (one that replaced a failed listener counts only after a poll reaches it), and (2) at least one host that is ready, has ci on and is allowed by the room's rule (`only`) answered the survey of the current pass. A pass whose store reads fail aborts with no write. Free slots, CPU, memory, disk, `--max` and the polls' capacity do not count, so a pool whose every slot is busy keeps its lease and its overflow jobs queue for a runner. A host with ci off, one that did not answer, no host at all, or listeners that keep failing before a poll lets it run out. A write sets the lease 15 minutes ahead and the next waits 5 minutes. The write comes right after the survey, before builds and the sweep, on its own 30-second timeout taken from the pass's outer context, so a slow host cannot delay it. The engine keeps only the time of its last write, so the first pass after a start writes at once. A restart, or a survey of up to 10 minutes, cannot open a gap. A survey longer than 10 minutes (it runs under the 20-minute `scaleLimit`) can still let the old lease lapse, since the write comes after it.

**GPUs.** A host reports the GPUs its CDI spec offers. The heartbeat's first good probe and then every thirtieth (about five minutes) runs `nvidia-ctk cdi list` and `nvidia-smi --query-gpu=index,name,memory.total`. A card counts when CDI lists `nvidia.com/gpu=<index>` for it, and its name and total memory come from `nvidia-smi`; a card CDI lists but `nvidia-smi` does not report has memory 0, which no request fits. Each tool runs under a five-second `timeout`, and a name may hold commas (the first field is the index and the last the memory). A host with neither tool reports no GPUs. The engine stores the list on the host (`gpus`: index, name, memory bytes). A probe whose SSH call fails leaves the last list; one that reaches the host stores what it found, and a tool that is missing, fails or times out counts as finding nothing, which empties the list. `host.list` carries each card with the VRAM boxes have requested of it (5.5), and the Home host card and Settings > Hosts show them. An exec-only host reports none. One-time host setup, for a host that has a card: install the NVIDIA Container Toolkit and run `nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml`.

**Offline engine relocation.** The owner can relocate the logical engine without renaming physical hosts with `dyna host relocate-engine --data /absolute/engine/data --from OLD --from-ssh user@REMOTE --to NEW`. `--from-ssh` is the former engine's now-remote SSH endpoint, never the destination's address; it must not be a loopback address. Both distinct registered hosts must exist. Active boxes (anything other than stopped, deleted, pending or failed) and subscription Runs on OLD refuse relocation with their IDs. The owner pauses affected rooms, refreshes running sessions and waits for stopped box rows before the final copy. The command takes the same exclusive data-folder lock as engine startup before opening the existing database, refuses a live engine, a missing store or a schema version other than this binary's, and never initializes or migrates a store. One transaction changes only OLD's SSH endpoint and the two hosts' derived `engine_host` flags (false for OLD, true for NEW). NEW keeps its existing SSH endpoint. Repeating the command is idempotent and emits no events. Unfinished `control-plane` host-exec runs or pending, approved or running host-exec operations targeting `control-plane` refuse relocation: the owner must finish or explicitly stop them first, since that logical target dispatches locally on whichever machine runs the engine. Historical operations and runs are never retargeted. Reversing OLD and NEW with the other former engine's remote endpoint supports rollback on the latest consistent stopped copy. Host IDs, placement and CI switches, GPU facts, room rules, boxes, host-exec jobs and grants, history, settings and secrets remain untouched. The CLI help is the owner's entry point; this is not an agent tool or a container/volume migration.

The data-folder lock excludes processes using the same folder, not two independent copies on different machines. The owner must stop and fence the old engine and its automatic restarters before making a consistent copy, and must never start both copies. Runtime sockets and SSH masters are recreated, not copied. Physical host data stays on its machine. Existing boxes on the former engine bind its old shared folder directly; forwards or a new FUSE mount cannot change that bind. They require an explicit, volume-preserving container recreation with the new shared mount before resuming writes. With the engine stopped and their stored rows already stopped, the owner can remove only those stopped containers, keeping every named volume; after room resume the normal missing-stopped-container path recreates them. Refresh alone does not replace an unchanged container, and session restart can discard a failed box's volume, so neither alone is a mount migration. Rollback after new writes requires copying the latest stopped engine data back first, not restarting a stale snapshot.

### 5.2 Images

`dyna-base` contains Debian, shell tools, git, Node, tini and, unpacked under `/opt/dyna/extensions`, pinned releases of the Chromium extensions uBlock Origin Lite and I still don't care about cookies (5.14); PiG holds no credentials. The image holds no agent: PiG is in the box agent, and Node stays for the package extensions PiG runs and Chromium's setup. Versioned Dyna binaries mount read-only from the host, independently of the image. Toolchains are room images (below); they start from `dyna-base` and finish as the non-root user. A room with several box images needs a compose file to say which one a session uses (5.16).

**Room images.** A room keeps its own images in files beside its instructions and add-ons (4.3): `<data>/rooms/<room>/images/<name>/` holds one image's `Containerfile` and its build context, with the `box-needs` and `size` files described here. An image is a folder of regular files (links are dropped) of at most 3 MiB packed, and it has no global scope. `dyna image save NAME PATH` packs a folder and replaces the room's image of that name whole; `dyna image list` shows each with its content id, file count and size; `dyna image show NAME` prints its `Containerfile` and its file list; `dyna image remove NAME` deletes it. The `image` tool has the same four actions (`save`, `list`, `show`, `remove`). A session changes its own room at once, as it does a room add-on, and the owner names any room with `--room`. A name is lowercase letters, digits, `_`, `.` and `-`. `ci-runner` is reserved for the room's CI runner image: its `Containerfile` has a `FROM` that names `${DYNA_CI_BASE}`, the runner image Dyna ships, while every other image names `${DYNA_BASE}`. A `Containerfile` is read as a builder reads it (comments and blank lines skipped, a trailing backslash joins lines, instruction names in any case) and holds to one rule: it declares `ARG DYNA_BASE` (`ARG DYNA_CI_BASE` for `ci-runner`) before its first `FROM`, as the build passes the base with `--build-arg`, and every `FROM` is `${DYNA_BASE}` (or `${DYNA_CI_BASE}`), optionally `AS name`; only `ARG` may come before the first `FROM`. A parser directive (`# syntax=`, `# escape=`, `# check=`) in the opening comment block is refused, since it changes how the builder reads the file or swaps its frontend. A heredoc body is read as instructions, so a heredoc line that starts with `FROM` is refused too. Multi-stage builds are allowed, but each stage starts from the base, so a `FROM` of another image or of an earlier stage is refused, as is a flag on `FROM`. The save also refuses a folder with no `Containerfile`, an unknown `box-needs` word and an invalid name, all with a typed `invalid` error. Limits keep one room from filling the engine's disk: an archive packs to at most 3 MiB, a room holds at most 16 images and 128 MiB unpacked, and a save over a limit is refused with the limit named. A name is checked once where it enters the store, so it cannot name a folder outside the room's images. A save moves the old image aside before it puts the new one in place, and every list, show, remove and save first restores an image a crash left aside (unless a newer one took its place) and clears unfinished saves. The Containerfile is read by a small parser here and not by BuildKit's, which Dyna does not vendor; its refusals err toward refusing. The content id is a SHA-256 over the folder's sorted paths, execute bits and contents, so saving a changed folder changes the image tag (the base's tag and this id, below) and saving the same folder changes nothing. Deleting a room removes its images with its folders. A host does not keep a mirror of the store, unlike add-ons: nothing on a host reads an image folder, since a box mounts none, and only a build needs the files, so the engine sends the build context with each build and the host never holds a copy that can go stale. A host offline during a save has nothing to catch up on: its next build uses the current folder and builds the new tag. This is the store alone: a box uses the room's sole box image, or the one its compose file names with `build`; scheduled runs name one too. **Migration:** the engine moves the legacy images of each room's primary repository (the room's sole repository when it has one and none is marked primary) from its `.dyna/images/*` (default branch or `dyna-images`) into the room, once per room. It runs in the background after start, and again before a box start, a scheduled run or a session start chooses an image, so a mirror that came late, or a repository added after the upgrade, is moved first (a repository with no mirror is fetched then; until it is moved its sessions use `dyna-base`, and the log says so). Only the primary repository is moved, so a room never holds the images of several repositories, which would make every session start without a compose `build` refuse for the choice; the image names of other repositories are logged as left, and `dyna image save` adds one. An image the room already has is never overwritten. A copied Containerfile is not rechecked, so one with a literal `FROM` still builds, but `dyna image save` of the same folder refuses it until fixed. **Final skips**, recorded as done: the room has the name, the name is `ci-runner`, the files exceed 3 MiB (checked with `git ls-tree -r -l` before any archive is read), or the store refuses the folder. Any other failure, such as a git error or the 2 minute limit on one repository's git calls, is logged and retried at the next call. Image ids are content hashes, not git tree ids, so each migrated image rebuilds once per host. A session with no repository takes the room's sole box image, where it took `dyna-base` before. Update an image with `dyna image save`. After that no repository is read for images. Settings > Images shows them too (5.13). **CI:** a repository's runners use the `ci-runner` image of the room it has CI on in (`repo ci`), or the shared runner image when that room has none; the image is never read from the repository, so a PR cannot change what its own job runs on. Each ci host builds it on the shared runner image (`DYNA_CI_BASE`) from the engine's copy at its next build-ahead or placement, so a host that missed a save builds the current image. The tag is the room and the content id, so a save builds again. Dyna names the entrypoint and the user (`1001:1001`) when it starts a runner, so a file cannot change them. Placement prefers a host that has the image. A failed build (its last lines of output, or a limit of 10 minutes per layer) is logged, the host runs that room's jobs on the shared image, and `host list` shows the failure until a build succeeds, retried every 5 minutes. A room whose image cannot be read shows the same, on every ci host, and does not stop other rooms or the lease. A layer no room has now is removed from each host. The layer build has network access and may use `RUN --network=host` and `COPY --from=` another image, as a room's box images do, and a file can overwrite files in the image, such as the entrypoint script, though never the entrypoint or user a runner starts with; it gains no more reach than its own room's runner has. A repository is in one CI room, so rooms cannot conflict.

Optional `box-needs` requests `nested-containers` or `kvm`; unknown needs fail loading. These grant additional devices or kernel access. Missing devices fail creation with a typed cause; admission does not select by device. Restrict rooms to suitable hosts. See [the container recipe](nested-containers.md). Image tags hash the base and image folder tree. Host builds are serial and reuse existing tags.

Podman gives a box's user mapping its own copy of a whole image the first time a container uses it, and every Podman command on that host waits meanwhile. Boxes that are already running keep running, because they do not call Podman, and heartbeats do not call it either. So a new image takes that copy once, right after its build, by creating and removing a throwaway container with the box mapping. The copy outlives the container. The step stops after two minutes: the image still works, and its first box makes the copy instead.


[Showing lines 1-290 of 1160 (50.0KB limit). Use offset=291 to continue.]