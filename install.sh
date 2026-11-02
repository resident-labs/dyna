#!/usr/bin/env bash
# Dyna has no installer now. This stub stays at the published curl URL on purpose:
# a missing file there would pipe nothing to bash, which exits 0 and installs nothing.
# Keep it failing until v2 has releases, and replace it then.
cat >&2 <<'EOF'
Dyna cannot be installed from main. Nothing was changed on this machine.

Dyna v2 is in next/. It has no release and no installer yet, and it is not ready
for general use. To build it from source, read next/docs/m1-run.md:
  https://github.com/resident-labs/dyna/blob/main/next/docs/m1-run.md
EOF
exit 1
