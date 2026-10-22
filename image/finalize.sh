#!/usr/bin/env bash
set -euo pipefail
source /recipe/versions.env
# These directories were created by the checkout owner before pacstrap.
chown root:root / /var /var/cache /var/cache/pacman /var/cache/pacman/pkg
chmod 0755 / /var /var/cache /var/cache/pacman /var/cache/pacman/pkg
# Write the same service definitions as native and hook installs, without enabling them.
/usr/local/bin/dyna engine install-services --write-only --unit-dir /etc/systemd/user
systemd-sysusers
systemd-tmpfiles --create --exclude-prefix=/dev
systemctl enable dyna-pacman-keyring.service
systemctl enable dyna-pacman-update.service
# WSL2 distros share one kernel and its binfmt_misc entries. systemd-binfmt
# would replace the entries of every distro, including WSLInterop. nspawn
# containers skip it anyway. dyna-wsl.service also unmounts binfmt_misc so
# that shutdown does not clear them.
systemctl enable dyna-wsl.service
systemctl mask systemd-binfmt.service
# Installers enable linger after they set the final user ID. On WSL2 all
# distros share one cgroup tree, so an early user@1000 can clash with another distro.
rm -f /var/lib/systemd/linger/dyna
# The managed container shares the host network. WSL supplies its own DNS file.
systemctl disable systemd-networkd systemd-resolved
rm -f /etc/systemd/network/80-container-host0.network /etc/resolv.conf
: > /etc/resolv.conf
if pacman -Q vulkan-intel vulkan-radeon >/dev/null 2>&1; then
 pacman -Rns --noconfirm vulkan-intel vulkan-radeon
fi
install -d -m 0755 /etc/dyna
printf 'version=%s\nbuild_date=%s\narchive_date=%s\ngit_commit=%s\n' \
 "$IMAGE_VERSION" "$BUILD_DATE" "$ARCHIVE_DATE" "$GIT_COMMIT" > /etc/dyna/image-release
pacman -Q > /output/packages.lock
# The build used a dated archive snapshot so builds repeat exactly. The
# installed system is a normal Arch machine: agents install what they need
# with pacman, and dyna-pacman-update.service upgrades it once on first boot.
cat > /etc/pacman.conf <<'CONF'
[options]
HoldPkg = pacman glibc
Architecture = auto
CheckSpace
ParallelDownloads = 5
SigLevel = Required DatabaseOptional
LocalFileSigLevel = Optional

[core]
Include = /etc/pacman.d/mirrorlist

[extra]
Include = /etc/pacman.d/mirrorlist
CONF
cat > /etc/pacman.d/mirrorlist <<'LIST'
# Live Arch mirrors. Edit or regenerate this list (for example with reflector).
Server = https://geo.mirror.pkgbuild.com/$repo/os/$arch
Server = https://mirror.rackspace.com/archlinux/$repo/os/$arch
Server = https://mirrors.kernel.org/archlinux/$repo/os/$arch
LIST
LC_ALL=C pacman -Qi | awk '/^Name / {name=$3} /^Installed Size / {print $4, $5, name}' > /output/package-sizes.txt
rm -rf /var/cache/pacman/pkg/* /root/.npm /usr/share/doc /usr/share/man /usr/share/info
find /usr/share/locale -mindepth 1 -maxdepth 1 ! -name 'en*' ! -name 'locale.alias' -exec rm -rf {} +
# Keep English Chromium UI resources, ICU data, fonts, and all renderers.
find /usr/lib/chromium/locales -type f ! -name 'en-*.pak' -delete
# Computer control uses the browser debugging protocol, not WebDriver.
rm -f /usr/bin/chromedriver
# Python recreates bytecode on demand. Keep its sources and development files.
find /usr/lib/python3.* -type d -name __pycache__ -prune -exec rm -rf {} +
# These are our Go binaries.
strip --strip-debug /usr/local/bin/dyna /usr/local/bin/dyna-desktop
rm -f /etc/ssh/ssh_host_* /var/lib/systemd/random-seed /var/lib/dbus/machine-id
: > /etc/machine-id
rm -rf /var/log/journal/* /var/log/pacman.log
