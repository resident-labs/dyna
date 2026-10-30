# Dyna recovery

## Read session state

Use `dyna subagent show <session> --all --tools` for conversation and tool details.
Use `dyna subagent output <session>` for final text; `--transcript` or `--raw`
shows the conversation or harness output. Recover earlier context with:

```sh
dyna subagent output <session> --turn 2 --transcript
dyna subagent history <session> --from-turn 1 --to-turn 4 --search 'parser'
dyna room read <room> <msg-id>
dyna room log <room> --all
```

## Model or provider failure

For usage limits (`429`), auth/quota errors, provider errors, or repeated crashes,
switch to a working profile without asking approval. Do not retry the blocked
profile. Ask only when no safe successor or handoff exists. Report the failure,
successor, and reset time with local timezone. Read `dyna guide steering` for alerts.

Prefer transfer. Preserve the worktree, branch, dirty changes, and history.
If transfer is unavailable, start a successor in the same room with a different
available model family. Root passes the existing room; Dyna sessions omit `--room`.
Keep one merge/install owner. A resumed source yields to its successor.

Give the successor the goal, decisions, next action, blockers, worktree, branch,
commits, dirty files, tests, PRs, evidence paths, and decisive message IDs.
Point to transcripts instead of copying them. The successor acknowledges the
handoff, checks branch and test state, and posts milestones.

## Restore dyna.db from a backup

The engine copies `dyna.db` with `VACUUM INTO` in two separate histories. Each
keeps its own newest 3 copies, named by UTC time
(`dyna-20261020T194339.405904588Z.db`), and neither can prune the other:

- `<data>/backups/` holds one copy a day, taken while the engine runs.
- `<data>/backups/pre-migration/` holds one copy per engine start, taken before a
  migration changes a store. A migration that fails at every start fills this
  folder, not the daily one.

After a failed migration the copy to restore is the newest one under
`backups/pre-migration/`: it holds the state the failed migration started from. A
daily copy can be almost a day old, and on a young data folder there may be none
at all.

The data folder is usually `~/.local/share/dyna`. Only the owner restores. A
restore loses every write made after the backup's time.

Turn runners, shells and CLI commands still write `dyna.db` directly. First pause
each room with `dyna room pause <room>`, and wait until `dyna subagent list`
shows no running turn. Otherwise their writes go to the moved file.

Stopping the engine is not enough for any move, copy-over or rewrite of
`dyna.db`. Each session's `dyna mcp` shell server and each `dyna shell _run`
supervisor opens the file itself and keeps writing to the old file after a swap;
new shell records then vanish and shell calls fail with `no such shell`. Before
the swap, list every holder with `lsof ~/.local/share/dyna/dyna.db*` and stop
them all.

```sh
systemctl --user stop dyna-engine.service
cd ~/.local/share/dyna
ls -l backups/dyna-*.db backups/pre-migration/dyna-*.db
                                   # newest is last in each list; pick one
sqlite3 backups/pre-migration/dyna-<time>.db 'PRAGMA integrity_check'
                                   # prints ok for the file you picked
mkdir -p broken && mv dyna.db* broken/       # dyna.db with its -wal and -shm
cp backups/pre-migration/dyna-<time>.db dyna.db && chmod 600 dyna.db
systemctl --user start dyna-engine.service
dyna room resume <room>                      # for each paused room
```

Name the file by its full path, including the folder. The same name shape exists
in both histories, so `backups/dyna-<time>.db` and
`backups/pre-migration/dyna-<time>.db` are different files. Check and copy the one
you chose; a restore from the wrong history loses writes you did not have to lose.

Move `-wal` and `-shm` with `dyna.db`; SQLite would apply an old `-wal` to the
restored file. A copy is safe here only because the engine is stopped. On
start, the engine migrates the restored file to its schema.

## Transfer

Transfer preserves session identity, hierarchy, children, files, history, and PR ownership.

```sh
dyna subagent transfer <session> --profile sol --model <model> --wait-seconds 30
dyna subagent transfer status <session> --json
dyna subagent transfer cancel <session>
dyna subagent transfer handoff <session> @handoff.txt --token <requested-token> --dir /actual/worktree --branch feature/task
dyna subagent transfer <session> --profile sol --retry
```

The default handoff window is 30 seconds; `--wait-seconds` accepts 0 to 120.
Zero or an already failed turn uses transcript context immediately. A running
turn holds the ask only until that window ends; the transfer then stops the turn
and uses the transcript, so it never waits without end. Write the
actual task directory and branch; the launch directory can be stale. A final
handoff may start with `[DYNA HANDOFF]`, then separate `Worktree: /absolute/path`
and `Branch: feature/name` lines.

A move between two Pi profiles needs no handoff. Pi keeps one session file per
conversation, so the transfer resumes that exact session with the new profile's
model, thinking level and provider account. The conversation and worktree stay,
and no handoff wait runs. If Pi cannot resume that session on the new model,
for example when the first launch left no session file or the history does not
fit the new window, the transfer starts the fresh conversation instead. Pi
compacts the session before a request that no longer fits the new model's
window, and Dyna runs the target profile's compact command first when its
threshold is due.

A move from Pi to Claude Code converts the Pi conversation into a new Claude
session file under the target account's resolved `CLAUDE_CONFIG_DIR` and
resumes it, so the same history continues without a handoff. Thinking blocks,
provider signatures, usage and model attribution do not cross; the target
model gets the user text, assistant text, tool calls and tool results. The
writer is pinned to the supported Pi and Claude Code versions. A conversion
failure, an unsupported version, or a first turn that cannot load the
converted session falls back to the written handoff, and `transfer status`
says why in its reason.

Repeating a request recovers its saved attempt. Use `--retry` after failure or
cancellation. Cancel only while waiting for handoff. Late handoffs cannot finish
another attempt. Dyna stops the old turn before replacement startup. Stored
posts survive startup.

Startup allows 60 seconds for model output. Startup failure restores the prior
profile, model, task directory, and native resume ID. Fix startup and retry,
choose another target, or post to resume the old model. Later provider errors
are normal turn failures.

## Consolidate sessions

```sh
dyna subagent consolidate <a> <b> --keep <a> --json
dyna subagent consolidate status <operation-id> --json
dyna subagent consolidate resume <operation-id>
dyna subagent consolidate cancel <operation-id>
```

Select 2 to 16 open sessions in one room. Optional `--profile` and `--model`
choose the survivor's model. All sources share one 30-second handoff window.
Failed sources use transcripts. Use the transfer handoff command above.
Never merge or delete source worktrees.

Sources archive only after successful startup. Histories, dirty files, branches,
and task directories remain. Children and active PR routing move to the survivor;
old addresses forward posts. Shared posts deliver once.
A combined author/reviewer role pauses review until an independent reviewer exists.

Repeat requests recover saved operations. Startup failure restores the previous
conversation; sources stay reserved until retry or cancel. Repeat an interrupted
cancel or resume. After accepted startup, resume completes storage changes.
Live PR guards still apply.

## Pause and finish

`dyna room pause <room>` holds new calls and delivery. Active calls finish;
workflows and shells continue. `dyna room resume <room>` releases pending posts
and preserves native identity. Repeat resume after interruption.
New transfers and consolidations refuse while paused; existing deadlines are preserved.

`dyna room session-controls <room> --json` shows state. A turn that ends with no
responsibilities makes its session dormant; the shell job the turn runs in does not
count, and no idle timer ever makes a session dormant. Owner-only
`dyna room clear-dormant <room> --json` closes dormant
sessions after checking responsibilities, pending input, and reservations.
Maintenance also closes sessions dormant and inactive for 7 days. History and
files remain; active work continues.
