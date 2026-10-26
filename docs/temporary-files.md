# Temporary files

On many hosts `/tmp` is a RAM disk. Files there use memory that agents and the
owner need. Dyna keeps the temporary files of what it starts on disk.

Every session and workflow worker gets these variables, and the shell jobs it
starts inherit them:

- `TMPDIR`, read by POSIX tools and by Go's `os.TempDir`.
- `TMP` and `TEMP`, read by tools that follow Windows conventions.
- `GOTMPDIR`, read by the Go toolchain for build folders.

Each points at its own folder, `~/.cache/dyna/tmp/<8 hex characters>`. The path
is short on purpose: tools bind unix sockets under `TMPDIR`, and a socket path
may hold at most 108 bytes. A profile that sets one of the variables keeps its
own value.

Hook runs and local CI checks have no session. They get the same variables set
to `~/.cache/dyna/tmp` itself.

A session's folder is removed with the session's scratch folder, seven days
after the session closes. The hourly maintenance pass removes the other entries
under `~/.cache/dyna/tmp` (hook and check files) when nothing in them has
changed for 72 hours, longer than any hook may run. Host checks of local CI run with their own folder there;
container checks keep their in-memory `/tmp`, which ends with the container. Only the engine's sockets and runtime
files stay in `XDG_RUNTIME_DIR`.

Maintenance also removes old `dyna-test-*` folders and the folders that killed
tools left in the system temporary folder (`dyna-go-env-*`, `dyna-pr-detail-*`
and similar).

Go tests of this repository still use the system temporary folder. A test
binary ignores the session's `TMPDIR`, because many tests bind unix sockets
under `t.TempDir()` and a longer path passes the socket limit.
