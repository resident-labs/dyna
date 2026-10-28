# Machines

## Builds and tests over SSH

Before you use another machine for builds or tests, run `dyna machine list`.
Use the SSH column as the exact `user@host`. Never guess a username.

An enabled machine whose Rooms column includes your room may be used over SSH for builds and tests.
`every room` includes your room. Do not use a machine limited to other rooms.

Use `dyna machine show <name>` to inspect one machine's roles, status, room
access, and capabilities. For Dyna-managed CI and pull-request checks, read
`dyna guide git`.
