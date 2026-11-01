# Run the M1 engine

Build the binary and the Pi package together. The engine reads `pi.tgz` from
the directory that contains its executable. A Go build alone does not create
this package.

From the repository root, with Go, Node and npm installed:

```sh
mkdir -p "$HOME/dyna-m1/bin"
(cd next && go build -o "$HOME/dyna-m1/bin/dyna" ./cmd/dyna)
next/third_party/pi/scripts/pi-pack.sh "$HOME/dyna-m1/bin/pi.tgz"
```

Keep both files in that directory. Build and test on the configured build node.
The package script builds the vendored Pi fork and verifies the package.

Create `$HOME/dyna-m1/profiles.json` with a profile for the model allowed by
the owner's proxy sign-in. For example:

```json
{
  "example": {
    "provider": "anthropic",
    "model": "claude-opus-5-5",
    "allowedModels": ["claude-opus-5-5"],
    "thinking": "xhigh"
  }
}
```

Start the engine. Use an absolute path on the host for `--host-data`. The
host check creates this directory.

```sh
"$HOME/dyna-m1/bin/dyna" engine \
  --data "$HOME/dyna-m1/data" \
  --profiles "$HOME/dyna-m1/profiles.json" \
  --host-data /absolute/path/on/the/host
```

In another terminal, register the host and create a room. Replace the example
SSH address with the owner's host.

```sh
dyna host add hermeslab --ssh user@host --owner-only-admin
dyna room create example
```

Use the returned room ID in `dyna session start --room ID --name worker
--profile example --goal '...' --brief '...'`. Read its events with
`dyna room log ID --follow`. The box can run `dyna post 'message'` without
a room flag; its socket identifies its room. Close the session with
`dyna session close SESSION_ID`.

M1 does not upgrade a running box across engine versions. Close all boxes
before an engine upgrade. The proxy must already have a valid sign-in in its
auth directory.

## Trial deployment and fix forward

Run `next/scripts/deploy-trial.sh --dry-run` on the trial host to build and
check the pair without installing it. The script accepts `--prefix`, `--node-bin`,
`--socket`, `--unit`, `--web-url` and `--web-host` for other installations.

v1's job runner (`dyna job run`) is gone from main, and nothing replaces it yet.
To deploy, SSH to the trial host and run the script from a clean checkout of
the commit you want. It needs Go and the Node in `--node-bin` on that host, and
it restarts the trial's user service:

```sh
git fetch origin && git checkout --detach origin/main
next/scripts/deploy-trial.sh
```

<!-- LEGACY(v1-transfer): this paragraph, the v1 job runner hook. Remove after the transfer. -->
A v1 schedule hook that still calls `dyna job run` works only while a v1 engine
runs. Replace it with a timer on the trial host that runs these commands, or
deploy by hand.

The trial never rolls back. Fix failures forward on main. An older binary
cannot open a store after a schema step. The engine keeps
`backups/pre-upgrade-v*.db` for an emergency restore; only the owner decides
whether to restore one.
