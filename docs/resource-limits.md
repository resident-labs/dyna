# Resource limits for agent work

Dyna runs each subagent turn and each detached workflow run in its own systemd scope below a shared `dyna.slice`. The kernel then shares the machine:

- Desktop apps win CPU when the machine is busy. They have `CPUWeight=100`, and `dyna.slice` has `CPUWeight=20`. An idle machine still gives agents every core.
- Agents share CPU fairly. One agent's large test run cannot starve the others.
- Memory above 65% of RAM makes the kernel slow down and reclaim Dyna work. At 80%, an out-of-memory kill hits a process inside Dyna, never the desktop.

Agents need no flag. Shells, tests and builds that an agent starts stay in its turn scope.

## Files and commands

- Dyna writes `~/.config/systemd/user/dyna.slice` on first use and runs `systemctl --user daemon-reload`. To keep local edits, remove the `# Managed by Dyna` line. Dyna then leaves the file alone.
- `systemd-cgtop /user.slice/user-$(id -u).slice/user@$(id -u).service/dyna.slice` shows live use per agent. Scopes are named `dyna-<session or run id>-<random>.scope`.
- Dyna never stops a scope. A scope ends when its last process exits. A long-lived service, such as a daemon started from a turn, should start in its own unit with `systemd-run --user --unit=<name> --slice=dyna.slice`.

## When it is off

Commands start unchanged when any of these is true:

- `DYNA_SLICE=off` is set.
- The OS is not Linux.
- The systemd user manager is not running (`$XDG_RUNTIME_DIR/systemd/private` is missing), as in most containers. Use the container's own limits there: `--cpus`, `--memory` and `--pids-limit`.
- `systemd-run` is not installed.
- The process is a Go test binary.

Code: `internal/agentslice`.
