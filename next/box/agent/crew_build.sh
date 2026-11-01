#!/bin/bash
# A crew's build lock. Links to this script lead PATH in a crew's box, so a worker's builds queue without the worker knowing.
# A command that only reads goes straight to the real tool. The box agent keeps the queue, and the lock is the connection on
# fd 9, so it ends with this script however that goes. Whoever holds the lock sets DYNA_BUILD_LOCK, so its children never wait
# for it. See crew_build.go.
tool=${0##*/}
plain=:$PATH:
plain=${plain//:${0%/*}:/:}
plain=${plain:1:-1}
real=$(PATH=$plain type -P "$tool") || {
  echo "$tool: command not found" >&2
  exit 127
}

first=
for arg; do
  case $arg in
    -* | +*) ;;
    *)
      first=$arg
      break
      ;;
  esac
done
build=1
case $tool in
  cargo) case $first in build | b | check | c | test | t | bench | run | r | clippy | doc | d | fix | install | rustc | rustdoc | nextest) ;; *) build= ;; esac ;;
  go) case $first in build | test | run | install | vet | generate) ;; *) build= ;; esac ;;
  npm | pnpm | yarn | bun) case $first in ls | list | ll | why | view | info | outdated | config | get | help | root | bin | prefix | audit | whoami | ping) build= ;; esac ;;
esac
case ${1-} in --version | -V | -v | --help | -h) build= ;; esac
[ -n "$build" ] || exec "$real" "$@"

if [ -z "${DYNA_BUILD_LOCK-}" ]; then
  { exec 9<>/dev/tcp/127.0.0.1/__DYNA_BUILD_LOCK_PORT__; } 2>/dev/null || exec "$real" "$@"
  export DYNA_BUILD_LOCK=held
  while read -r -u 9 answer && [ "$answer" = wait ]; do
    echo "[dyna] $tool is waiting for the box's build lock" >&2
  done
fi

# Worktrees share one Cargo target, where a crate has the same name in every tree and cargo judges it by file times alone. A
# file written before this build queued looks older than the last tree's build, so cargo would hand this tree that tree's
# binary. When the tree changes, its files become newer than anything built, and cargo rebuilds its crates. Dependencies have
# no reason to rebuild, which is what sharing the target is for.
if [ "$tool" = cargo ] && [ -n "${CARGO_TARGET_DIR-}" ] && tree=$(git rev-parse --show-toplevel 2>/dev/null); then
  if [ "$(cat "$CARGO_TARGET_DIR/.dyna-tree" 2>/dev/null)" != "$tree" ]; then
    (cd "$tree" && git ls-files -z --cached --others --exclude-standard | xargs -0 -r touch -c --)
    mkdir -p "$CARGO_TARGET_DIR"
    echo "$tree" >"$CARGO_TARGET_DIR/.dyna-tree"
  fi
fi

"$real" "$@" 9>&-
