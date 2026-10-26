# Temporary files

On many hosts `/tmp` is a RAM disk. Files there use memory that agents and the
owner need. Dyna keeps the temporary files of what it starts on disk.

Every session, workflow worker and shell job a session starts gets these
variables, all set to the folder `tmp` inside the session's scratch folder
(`$DYNA_SCRATCH/tmp`, under `~/.cache/dyna/scratch`):

- `TMPDIR`, read by POSIX tools and by Go's `os.TempDir`.
- `TMP` and `TEMP`, read by tools that follow Windows conventions.
- `GOTMPDIR`, read by the Go toolchain for build folders.

The scratch sweep removes `tmp` with the rest of the session's scratch, seven
days after the session closes. Hook runs and local CI checks have no session.
They get the same variables set to `~/.cache/dyna/tmp`. The hourly maintenance
pass removes entries there that are older than six hours.

A profile can set its own value for any of these variables. Only the engine's
sockets and runtime files stay in `XDG_RUNTIME_DIR`.

Maintenance also removes old `dyna-test-*` folders and the folders that killed
tools left in the system temporary folder (`dyna-go-env-*`, `dyna-pr-detail-*`
and similar).

Go tests of this repository still use the system temporary folder, because many
bind unix sockets under `t.TempDir()` and a longer path passes the 108-byte
socket limit.
