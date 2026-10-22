#!/usr/bin/env bash
set -euo pipefail
source /recipe/versions.env
cp /output/pacman.conf /etc/pacman.conf
groupadd -g 1000 dyna
useradd -m -u 1000 -g 1000 -s /bin/bash dyna
cp -r --preserve=mode,timestamps /recipe/overlay/. /
chown root:root /
chmod 0755 /
chmod 0440 /etc/sudoers.d/dyna
install -Dm755 /output/dyna /usr/local/bin/dyna
install -Dm755 /output/dyna-desktop /usr/local/bin/dyna-desktop
# Codex and Claude Code are not baked in. Dyna installs a harness CLI as a
# private bundle the first time an account or session needs it. The bundle
# installer runs npm, so nodejs and npm stay in packages.txt.
systemctl set-default multi-user.target
/bin/bash /recipe/finalize.sh
