# Nodes

## Builds and tests over SSH

Before you use another node for builds or tests, run `dyna node list`.
Use the SSH column as the exact `user@host`.

An enabled node whose Rooms column includes your room may be used over SSH for builds and tests.
`every room` includes your room. Choose a node available to your room.

Use `dyna node show <name>` to inspect one node's roles, status, room
access, and capabilities. For Dyna-managed CI and pull-request checks, read
`dyna guide git`.
