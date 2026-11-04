# dyna

## What Dyna is

Dyna is a self-hosted control plane for fleets of long-running coding agents. A single Go engine, backed by a SQLite store that holds an append-only event log, schedules each agent session into its own rootless Podman container ("box") on SSH-attached hosts, placed by CPU and memory headroom, with pools for elastic nodes. Inside each box a Go box agent supervises an agent harness (Pi), feeds it turns, steers and context diffs over a socket tunnelled to the engine, and reports transcripts and activity back.

Sessions form a tree inside rooms: a root per room delegates to child sessions, which hold goals. All communication is engine-routed posts and mentions with sender-authenticated frames. The engine also runs the cross-cutting services agents share:

- a model proxy that pools subscription accounts by quota;
- a git mirror and route for repos;
- a GitHub watcher that wakes sessions on PR, review and CI changes;
- a self-hosted CI runner pool on the same nodes;
- host exec for registered machines;
- add-ons (skills, Pi packages, MCP servers);
- a Svelte web app.

It is effectively Kubernetes plus Slack for agents, specialised: per-session containers instead of pods, an agent hierarchy instead of deployments, and posts instead of RPC between workers.

## Status

This repository holds Dyna v2, a rewrite. It is not released and is not ready
for general use. There is no release, no installer and no update path yet:
`install.sh` stops with an explanation.

Dyna v1 is retired and main has no v1 code. Its last source is tag
[`v1-final`](https://github.com/resident-labs/dyna/tree/v1-final).

## Layout

- [`next/`](next/): the v2 Go module, web app and vendored forks. It moves to the
  repository root in a later change. The design is in
  [next/docs/spec.md](next/docs/spec.md).
- [`relay/`](relay/): the Resident Labs Connect relay, a Cloudflare worker.
  v2 keeps its wire protocol.
- `testdata/connect/`: wire vectors shared by `relay/` and `next/engine/connect`.
- `.dyna/images/`: the image this repository's own boxes build from.

## Build from source

[next/docs/m1-run.md](next/docs/m1-run.md) builds the engine and its Pi package
and runs them.

## Moving from v1

A v2 engine refuses a v1 data folder, and there is no import: the rooms were
rebuilt by hand. Tag
[`v2-import-last`](https://github.com/resident-labs/dyna/tree/v2-import-last)
keeps the bundle check and its guide,
[next/docs/migrate.md](https://github.com/resident-labs/dyna/blob/v2-import-last/next/docs/migrate.md).

## License

MIT — see [LICENSE](LICENSE).
