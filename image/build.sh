#!/usr/bin/env bash
# Run as the checkout owner. Only rootfs operations use sudo.
set -euo pipefail
recipe=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$recipe")
source "$recipe/versions.env"
out=${1:?Usage: image/build.sh OUTPUT_DIRECTORY (outside the checkout)}
mkdir -p "$out"
out=$(realpath "$out")
build_name="dyna-build-$(printf %s "$out" | sha256sum | cut -c1-12)"
case "$out/" in "$repo/"*) echo "Output must be outside the checkout" >&2; exit 1;; esac
[[ $(uname -m) == x86_64 ]] || { echo "Only x86_64 is supported" >&2; exit 1; }
for tool in pacstrap systemd-nspawn zstd go npm pkg-config curl gpgv; do command -v "$tool" >/dev/null; done
if [[ ${2:-} == --finalize-only ]]; then
 [[ -x "$out/rootfs/usr/bin/bash" && -f "$out/rootfs/etc/dyna/image-release" ]] || {
  echo "--finalize-only requires a rootfs from this recipe" >&2; exit 1;
 }
elif [[ -e "$out/rootfs" ]]; then
 [[ ${2:-} == --resume-download && ! -e "$out/rootfs/usr/bin/bash" ]] || {
  echo "Use a fresh output directory (or --resume-download after a download failure)" >&2; exit 1;
 }
fi
run_root() {
 printf 'sudo' >> "$out/sudo-commands.log"
 printf ' %q' "$@" >> "$out/sudo-commands.log"
 printf '\n' >> "$out/sudo-commands.log"
 sudo "$@"
}
start=$SECONDS
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo" show -s --format=%ct HEAD)}
git_commit=$(git -C "$repo" rev-parse HEAD)
build_date=$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)
if [[ ${2:-} == --finalize-only ]]; then
 run_root systemd-nspawn --machine="$build_name" --register=no --resolv-conf=off --timezone=off -D "$out/rootfs" \
  --bind-ro="$recipe:/recipe" --bind="$out:/output" \
  --setenv="BUILD_DATE=$build_date" --setenv="GIT_COMMIT=$git_commit" \
  /bin/bash -c 'cp -r --preserve=mode,timestamps /recipe/overlay/. /; /bin/bash /recipe/finalize.sh'
else
(cd "$repo/web" && npm ci && npm run build)
(cd "$repo" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags=-buildid= -o "$out/dyna" ./cmd/dyna)
(cd "$repo/desktop" && bash build.sh "$out/dyna-desktop")
"$out/dyna" engine run --help > "$out/serve-help.txt" 2>&1
cat > "$out/pacman.conf" <<CONF
[options]
Architecture = x86_64
SigLevel = Required DatabaseOptional
CheckSpace
ParallelDownloads = 5
DisableDownloadTimeout
[core]
Server = https://archive.archlinux.org/repos/$ARCHIVE_DATE/\$repo/os/\$arch
[extra]
Server = https://archive.archlinux.org/repos/$ARCHIVE_DATE/\$repo/os/\$arch
CONF
mapfile -t packages < <(sed '/^#/d; /^$/d' "$recipe/packages.txt")
mkdir -p "$out/rootfs/var/cache/pacman/pkg"
# The archive database names a newer rebuild under the same aml filename.
# Only this explicit recovery may use a live mirror; all bytes stay pinned.
aml_file=aml-1.0.0-1-x86_64.pkg.tar.zst
curl -fLsS --retry 3 "$AML_RECOVERY_URL" -o "$out/$aml_file"
curl -fLsS --retry 3 "$AML_RECOVERY_URL.sig" -o "$out/$aml_file.sig"
printf '%s  %s\n' "$AML_RECOVERY_SHA256" "$out/$aml_file" | sha256sum --check --strict
gpgv --keyring /etc/pacman.d/gnupg/pubring.gpg "$out/$aml_file.sig" "$out/$aml_file"
cp "$out/$aml_file" "$out/$aml_file.sig" "$out/rootfs/var/cache/pacman/pkg/"
mkdir -p "$out/empty-hooks"
# Finalize users and temporary paths after correcting staging ownership.
# A container image has no kernel or initramfs to generate.
for hook in 20-systemd-sysusers.hook 21-systemd-tmpfiles.hook 90-mkinitcpio-install.hook; do
 ln -sf /dev/null "$out/empty-hooks/$hook"
done
run_root pacstrap -C "$out/pacman.conf" -G -M "$out/rootfs" "${packages[@]}" --hookdir "$out/empty-hooks"
# Keep the archive repositories inside the image for later package operations.
# /recipe and /output are temporary mounts, excluded from the archive.
run_root systemd-nspawn --machine="$build_name" --register=no --resolv-conf=copy-host --timezone=off -D "$out/rootfs" \
 --bind-ro="$recipe:/recipe" --bind="$out:/output" \
 --setenv="BUILD_DATE=$build_date" --setenv="GIT_COMMIT=$git_commit" /bin/bash /recipe/provision.sh
fi
run_root systemd-nspawn --machine="$build_name" --register=no --resolv-conf=off --timezone=off -D "$out/rootfs" \
 --bind="$out:/output" /bin/bash -c ': > /etc/machine-id; exec tar "$@"' bash --sort=name --numeric-owner --xattrs --acls \
 --mtime="@$SOURCE_DATE_EPOCH" --clamp-mtime -cpf /output/dyna.tar \
 --exclude=./output --exclude=./recipe --exclude=./proc --exclude=./sys \
 --exclude=./dev --exclude=./run --exclude=./tmp --exclude=./etc/hostname -C / .
# Bound compression threads while retaining the shipped format.
zstd -T2 -19 --long --no-progress -f "$out/dyna.tar" -o "$out/dyna.tar.zst"
(cd "$out" && sha256sum dyna.tar.zst > dyna.tar.zst.sha256)
printf 'Build completed in %s seconds\n' "$((SECONDS-start))" | tee "$out/build-time.txt"
stat -c '%n %s bytes' "$out/dyna.tar" "$out/dyna.tar.zst"
