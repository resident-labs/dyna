#!/bin/bash
# Starts one ephemeral runner. The image is read-only and $HOME is a size-capped
# tmpfs, so everything a job writes dies with the container.
set -euo pipefail

# The container has no network. This local port is the way out, and the proxy
# behind the socket reaches public addresses only.
socat TCP-LISTEN:3128,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:/run/dyna-egress/egress.sock &

# The runner writes beside its own binaries, so bin/ is copied. The large
# externals/ folder is only read, and a link keeps it out of the tmpfs.
root=$HOME/actions-runner
mkdir "$root"
cp -a /opt/actions-runner/bin /opt/actions-runner/*.sh /opt/actions-runner/*.template "$root"/
ln -s /opt/actions-runner/externals "$root/externals"
cd "$root"

# The runner reads and clears ACTIONS_RUNNER_INPUT_JITCONFIG. A runner that never
# gets a job still ends here, so no container outlives this bound.
exec timeout --kill-after=30s 6h ./run.sh
