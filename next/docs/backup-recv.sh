#!/bin/sh
# Forced command for the Dyna backup key on the second machine. Stdin is one
# age file, and SSH_ORIGINAL_COMMAND is its size in bytes. The key can only add
# a copy here: it gets no shell and cannot read older copies. This machine's
# clock names each copy and decides its age, so a bad upload cannot overwrite
# or evict a good copy.
set -euf
umask 077
cd "$(dirname "$0")"
keep=14
limit_gib=16
floor_gib=50
set -- ${SSH_ORIGINAL_COMMAND:-}
[ $# -eq 1 ] || exit 64
bytes=$1
case $bytes in '' | *[!0-9]*) exit 64 ;; esac
[ "${#bytes}" -le 12 ] || exit 64
[ "$bytes" -le $((limit_gib * 1073741824)) ] || exit 64
# Exit 66 tells the engine that this machine has no room; other work here keeps its disk.
free_kib=$(df -Pk . | awk 'NR==2 {print $4}')
[ $((free_kib - bytes / 1024)) -ge $((floor_gib * 1048576)) ] || exit 66
name=dyna-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz.age
tmp=.$name.partial
trap 'rm -f "$tmp"' EXIT
head -c "$bytes" >"$tmp"
[ "$(wc -c <"$tmp")" -eq "$bytes" ] || exit 65
mv "$tmp" "$name"
# Remove a copy only when it is past the newest 14 and also over 14 days old.
ls -1t | grep -E '^dyna-.*\.tar\.gz\.age$' | tail -n +$((keep + 1)) | xargs -r -I{} find {} -mtime +$((keep - 1)) -delete
