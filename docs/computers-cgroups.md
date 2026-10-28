# Computer resource controls on Linux

The shared computer daemon uses cgroup v2 when it runs in the dedicated,
delegated `dyna-computers.service`. Before starting computer processes, it
moves itself into the service’s `daemon` leaf. It then enables
`+memory +cpu +pids` at the service root. Without room slices, each computer gets a sibling leaf.
A dedicated `dyna-computers-*.service` instance also supports these controls.
A direct daemon start outside these services uses tracked processes instead.

Each computer leaf has these settings:

| Control | Value | Effect |
| --- | --- | --- |
| `memory.high` | `2147483648` bytes (2 GiB) | Reclaims memory and slows allocations above this threshold. |
| `memory.oom.group` | `1` | Treats the computer as one group during a memory cgroup out-of-memory kill. |
| `cpu.weight` | `100` | Gives each busy computer the same CPU share. |

The 2 GiB threshold gives a browser and desktop useful space while applying
pressure before many idle computers exhaust the shared budget. It is a soft
threshold, not a hard memory cap or reserved memory. Set
`DYNA_COMPUTER_MEMORY_HIGH` in the daemon environment to a positive decimal
byte count to change it. Empty, zero, negative, and invalid values use the
default. Dyna passes this variable through when starting the daemon with systemd.

The daemon does not change any ancestor settings. `dyna.slice` retains
`CPUWeight=20`, `MemoryHigh=65%`, and `MemoryMax=80%`, as defined in
`internal/agentslice/agentslice.go`. Equal computer weights divide the CPU
available within that outer budget. The daemon leaf also has the default
weight of 100. Viewer state does not change weights in this implementation.
The pids controller provides accounting; this change adds no per-computer
`pids.max`. It also adds no per-computer `memory.max`.

## Browser attachment

The Chromium wrapper joins the computer leaf before browser execution.
This includes agent-browser and Playwright launches that select the injected
`DYNA_CHROMIUM_EXECUTABLE`. Browser children inherit their parent’s group.

A headed Chromium browser can open its display connection from a GPU child.
The daemon follows known Chromium child processes to the browser root, then
moves that browser and its current descendants into the computer leaf. It stops
before the shell or agent that launched the browser. Chromium can request its
own systemd application scope for portal identification after launch; following
the display peer back to the browser root also restores that root to its computer.
See [Chromium’s scope setup](https://github.com/chromium/chromium/blob/main/components/dbus/xdg/systemd.cc). Unknown or unreadable
process trees keep the original peer PID. Custom browser names may require
explicit support. Status refresh also attaches private agent-browser daemons.

Status exposes `memory_current_kib`, from `memory.current`. This measures memory
charged to the group, including shared memory and cache. It differs from summed
resident set size. Moving an existing browser does not transfer all earlier
memory charges. The wrapper joins before execution for more complete accounting.

Process attachment checks process start times again before each move. A
process can still exit or fork during attachment. Attachment errors retain
tracked-process cleanup. A browser connected to multiple computers should
be avoided because one process can belong to only one cgroup.

## Failure behavior and checks

Setup checks all three controllers before moving the daemon. It refuses a
service root that contains another process. If movement or controller setup
fails, registration continues with tracked processes. The daemon can remain
in its leaf after a controller setup failure; that leaf remains within the
original service budget. A computer leaf is used only after all limits and
`cgroup.kill` are available. Failed setup does not change ancestor limits.

Tests cover controller selection, daemon placement, shared roots, unavailable
controls, configured thresholds, equal computer weights, and Chromium parent
selection. `TestCgroupDelegatedIntegration` also checks real kernel controls.
It requires `DYNA_TEST_CGROUP_DELEGATION=1` and a fresh delegated systemd test
service whose name starts with `dyna-cgroup-test-`. Run its compiled test
binary directly in that service, with bounded CPU, memory, and task limits.
The ordinary cgroup unit tests do not move the test runner’s cgroup or start a browser.
