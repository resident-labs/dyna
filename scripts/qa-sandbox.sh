#!/usr/bin/env bash
# Run a branch engine and browser together without the owner's home or network.
set -euo pipefail
if ! command -v bwrap >/dev/null 2>&1; then
  echo 'QA sandbox requires bubblewrap (bwrap); install it before running QA.' >&2
  exit 1
fi
if (( $# < 8 )) || [[ $7 != -- ]]; then
  echo 'Usage: qa-sandbox.sh ROOT BINARY WEB MODULE NODE CHROMIUM -- COMMAND [ARGS...]' >&2
  exit 2
fi
root=$(realpath -e -- "$1")
scratch=$(realpath -e -- "${DYNA_SCRATCH:?DYNA_SCRATCH must name the scratch directory}")
case "$root/" in "$scratch/"*) ;; *) echo 'QA root must be inside DYNA_SCRATCH.' >&2; exit 2;; esac
if [[ $root == "$scratch" || ! -d $root ]]; then
  echo 'QA root must be a dedicated directory inside DYNA_SCRATCH.' >&2; exit 2
fi
shift
binary=$(realpath -e -- "$1")
web=$(realpath -e -- "$2")
module=$(realpath -e -- "$3")
node=$(realpath -e -- "$4")
chromium=$(realpath -e -- "$5")
binds=()
for artifact in "$binary" "$web" "$(dirname "$module")" "$node" "$chromium"; do
  path=$(realpath -e -- "$artifact")
  # Bind only the requested artifact, not its owner-controlled parent.
  binds+=(--ro-bind "$path" "$path")
done
shift 6
command_path=$(realpath -e -- "$1")
binds+=(--ro-bind "$command_path" "$command_path")
shift
mkdir -p "$root"/{home,config,data,cache,runtime,tmp}
chmod 700 "$root" "$root"/{home,config,data,cache,runtime,tmp}
exec bwrap --die-with-parent --new-session --unshare-user --unshare-pid --unshare-net --unshare-ipc --unshare-uts \
  --clearenv --ro-bind /usr /usr --symlink usr/bin /bin --symlink usr/lib /lib --symlink usr/lib /lib64 \
  --proc /proc --dev /dev --tmpfs /tmp --tmpfs /run \
  --ro-bind /etc/fonts /etc/fonts --ro-bind /etc/ssl /etc/ssl \
  --bind "$root" "$root" --bind "$root" /qa "${binds[@]}" \
  --setenv HOME /qa/home --setenv XDG_CONFIG_HOME /qa/config \
  --setenv XDG_DATA_HOME /qa/data --setenv XDG_CACHE_HOME /qa/cache \
  --setenv XDG_RUNTIME_DIR /qa/runtime --setenv DYNA_DATA_DIR /qa/data \
  --setenv DYNA_SCRATCH /qa --setenv TMPDIR /qa/tmp \
  --setenv DYNA_QA_ENGINE "$binary" --setenv DYNA_QA_WEB "$web" --setenv DYNA_QA_MODULE "$module" \
  --setenv DYNA_QA_NODE "$node" --setenv DYNA_QA_CHROMIUM "$chromium" \
  --setenv PATH /usr/bin:/bin --setenv DYNA_NO_AUTO_UPDATE 1 --chdir "$root" -- "$command_path" "$@"
