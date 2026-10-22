#!/usr/bin/env bash
set -euo pipefail
out=${1:?Usage: image/smoke.sh BUILD_OUTPUT_DIRECTORY}
out=$(realpath "$out")
extract_name="dyna-extract-$(printf %s "$out" | sha256sum | cut -c1-12)"
run_root() {
 printf 'sudo' >> "$out/sudo-commands.log"
 printf ' %q' "$@" >> "$out/sudo-commands.log"
 printf '\n' >> "$out/sudo-commands.log"
 sudo "$@"
}
name=dyna-imgtest
if machinectl show "$name" >/dev/null 2>&1 || machinectl show-image "$name" >/dev/null 2>&1; then
 echo "$name already exists; refusing to touch it" >&2; exit 1
fi
archive=dyna.tar.zst
tar_flags=(--zstd)
if [[ ${2:-} == --uncompressed ]]; then
 archive=dyna.tar
 tar_flags=()
fi
[[ -f "$out/$archive" ]]
# /tmp can be nosuid. Use the normal machine image location for the boot check.
# Refuse any existing image before creating this temporary test directory.
run_root systemd-nspawn --machine="$extract_name" --register=no --resolv-conf=off --timezone=off -D "$out/rootfs" \
 --bind="$out:/output" --bind=/var/lib/machines:/machines \
 /bin/bash -c 'set -e; mkdir /machines/dyna-imgtest; tar "$@"; test ! -e /machines/dyna-imgtest/var/lib/systemd/linger/dyna; mkdir -p /machines/dyna-imgtest/var/lib/systemd/linger; touch /machines/dyna-imgtest/var/lib/systemd/linger/dyna' bash \
 "${tar_flags[@]}" -xpf "/output/$archive" -C /machines/dyna-imgtest
cleanup() {
 run_root machinectl terminate "$name" 2>/dev/null || true
 wait "${boot_pid:-}" 2>/dev/null || true
}
trap cleanup EXIT
run_root systemd-nspawn --boot --machine="$name" -D "/var/lib/machines/$name" \
 --network-veth --resolv-conf=off --timezone=off > "$out/boot.log" 2>&1 &
boot_pid=$!
for ((attempt=0; attempt<60; attempt++)); do
 if run_root machinectl shell "root@$name" /usr/bin/true >> "$out/boot-ready.log" 2>&1; then break; fi
 sleep 1
done
run_root machinectl shell "root@$name" /bin/bash -lc '
set -euo pipefail
timeout 120 systemctl is-system-running --wait
test "$(cat /proc/1/comm)" = systemd
test "$(id -u dyna)" = 1000
test "$(id -g dyna)" = 1000
test "$(systemctl is-enabled systemd-binfmt.service || true)" = masked
systemctl is-active user@1000.service
test "$(systemctl --global is-enabled dyna-engine.service)" = disabled
dyna --version
! command -v claude
! command -v codex
npm --version
labwc --version
command -v foot
dyna engine run --help
echo DYNA_ROOT_SMOKE_OK
' | tr -d '\r' | tee "$out/smoke.log"
grep -Fxq DYNA_ROOT_SMOKE_OK "$out/smoke.log"
run_root machinectl shell "dyna@$name" /bin/bash -lc '
set -euo pipefail
sudo -n true
export XDG_RUNTIME_DIR=/run/user/1000
cat > /tmp/dyna-wayland-smoke.sh << "CHECK"
#!/bin/sh
set -eu
wlr-randr > /tmp/software-outputs.txt
grim /tmp/software.png
test -s /tmp/software.png
echo "Software labwc and grim passed" > /tmp/software-result.txt
CHECK
chmod +x /tmp/dyna-wayland-smoke.sh
WLR_BACKENDS=headless WLR_HEADLESS_OUTPUTS=1 WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1 \
 timeout 45 labwc -S /tmp/dyna-wayland-smoke.sh > /tmp/labwc-smoke.log 2>&1
cat /tmp/software-result.txt
dyna engine run --addr 127.0.0.1:7487 --card-addr 127.0.0.1:7488 \
 --no-tunnel --no-tailscale --no-push > /tmp/dyna-engine-smoke.log 2>&1 &
serve_pid=$!
trap "kill $serve_pid 2>/dev/null || true" EXIT
http_status=""
for ((attempt=0; attempt<30; attempt++)); do
 http_status=$(curl -sS --max-time 2 -o /tmp/dyna-engine-response.html -w "%{http_code}" http://127.0.0.1:7487/ 2>/dev/null || true)
 if [[ "$http_status" == 200 ]]; then break; fi
 kill -0 "$serve_pid" 2>/dev/null || { cat /tmp/dyna-engine-smoke.log; exit 1; }
 sleep 1
done
[[ "$http_status" == 200 ]] || { cat /tmp/dyna-engine-smoke.log; exit 1; }
echo "Dyna serve returned HTTP 200"
kill "$serve_pid"
wait "$serve_pid" || true
trap - EXIT
timeout 45 chromium --headless --no-sandbox --disable-gpu --disable-dev-shm-usage --dump-dom "data:text/html,<p>Dyna smoke passed</p>" 2>/tmp/chromium-smoke.log | grep "Dyna smoke passed"
echo DYNA_USER_SMOKE_OK
' | tr -d '\r' | tee -a "$out/smoke.log"
grep -Fxq DYNA_USER_SMOKE_OK "$out/smoke.log"
cleanup
trap - EXIT
run_root machinectl remove "$name"
echo "Smoke test passed" | tee -a "$out/smoke.log"
