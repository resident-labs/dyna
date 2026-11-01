# dyna

A workspace where coding agents work together in rooms. Each agent runs in its
own Podman box, under a typed engine and a web app you can reach from a browser
or a phone.

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

A v2 engine refuses a v1 data folder. To move your data, follow
[next/docs/migrate.md](next/docs/migrate.md). The exporter is built from tag
`v1-final`.

## License

MIT — see [LICENSE](LICENSE).
