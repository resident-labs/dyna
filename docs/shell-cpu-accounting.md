# Shell command CPU accounting

Shell CPU activity is currently unsupported on all hosts. The shared activity
signal reports this explicitly. A missing or expired publication is unknown.
Neither state means that a command used zero CPU.

The supervisor and command currently share a scope. Its cgroup v2 `cpu.stat`
also counts supervisor work, including activity publication. Process-tree
snapshots can miss short-lived or detached descendants. Neither source safely
measures command-only CPU.

A future implementation needs a verified, delegated cgroup v2 child for the
command alone. It must place the command in that child atomically at launch,
before it can fork or do work. It must verify that descendants cannot escape
the measured boundary, read cumulative `cpu.stat`, and return unknown if
placement, attribution, reading, or publication fails. The design must keep
the supervisor outside that child and preserve caller identity and process
group stopping. It also needs cleanup and safe behavior across engine restarts.

The current shell scope does not guarantee delegated child cgroups. Do not
infer support from the presence of cgroup v2 or from a writable parent alone.
Hosts without verified delegation must remain unsupported.

## Local output inactivity notices

The engine can report continuously observed local shell output inactivity.
The notice does not report CPU inactivity or claim a deadlock. It requires a
fresh, running output observer for the full quiet period. Missing coverage
resets that period. The raw output observer sees bytes before line buffering
and truncation.

The setting `shells.silenceMinutes` sets the threshold. The default is 30
minutes. Set it to 0 to turn notices off. A shell gets one notice per quiet
period. New output starts a new period.

Agent turns and workflow steps do not yet provide proven observer liveness.
Workflow command stderr and hosted partial frames also remain unobserved.
Those executions do not get inactivity notices until their coverage is proven.
Command-only CPU accounting remains unsupported.
