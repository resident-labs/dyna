#!/usr/bin/env bash
# Dyna has no installer now. This stub stays at the published curl URL on purpose:
# a missing file there would pipe nothing to bash, which exits 0 and installs nothing.
# Keep it failing until v2 has releases, and replace it then.
# LEGACY(v1-transfer): the v1 lines of this message, not the failing stub itself. Remove after the transfer.
cat >&2 <<'EOF'
Dyna cannot be installed from main. Nothing was changed on this machine.

Dyna v1 is retired, and main has no v1 code. Its last source is tag v1-final.

Dyna v2 is in next/. It has no release and no installer yet, and it is not ready
for general use. To build it from source, read next/docs/m1-run.md:
  https://github.com/resident-labs/dyna/blob/main/next/docs/m1-run.md

To move a v1 store to v2, read next/docs/migrate.md. The exporter is built from
tag v1-final:
  https://github.com/resident-labs/dyna/blob/main/next/docs/migrate.md
EOF
exit 1
