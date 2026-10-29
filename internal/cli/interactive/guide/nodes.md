# Nodes

## Builds and tests over SSH

Before you use another node for builds or tests, run `dyna node list`.
Use the SSH column as the exact `user@host`. Never guess a username.

An enabled node whose Rooms column includes your room may be used over SSH for builds and tests.
`every room` includes your room. Do not use a node limited to other rooms.

Use `dyna node show <name>` to inspect one node's roles, status, room
access, and capabilities. For Dyna-managed CI and pull-request checks, read
`dyna guide git`.
