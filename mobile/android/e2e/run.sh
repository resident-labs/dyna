#!/usr/bin/env bash
# Headless emulator test for the call app: pair, join, press Home, turn the screen off,
# and check the call keeps working. Needs KVM, the Android SDK (ANDROID_HOME) with the
# emulator and a system image, and JDK 17 (JAVA_HOME). The engine is e2e/fakeengine.
#
#   ANDROID_HOME=... JAVA_HOME=... mobile/android/e2e/run.sh [background-seconds]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
bg=${1:-120}
image="system-images;android-34;google_apis;x86_64"
sdk=${ANDROID_HOME:?set ANDROID_HOME}
export PATH="$sdk/cmdline-tools/latest/bin:$sdk/platform-tools:$sdk/emulator:$PATH"
export ANDROID_AVD_HOME=${ANDROID_AVD_HOME:-$HOME/.android/avd} # avdmanager and the emulator must agree
mkdir -p "$ANDROID_AVD_HOME"
work=$(mktemp -d)
pids=()
cleanup() {
  # The emulator must not outlive the test: it is heavy. Ask it to stop, then kill what is left.
  adb emu kill >/dev/null 2>&1 || true
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  pkill -f "qemu-system.*-avd dyna_e2e" 2>/dev/null || true
  if [ -z "${KEEP:-}" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT

stat() { curl -fs localhost:17457/stats | python3 -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }
fail() { echo "FAIL: $*"; adb exec-out screencap -p >"${TMPDIR:-/tmp}/dyna-e2e-fail.png" 2>/dev/null && echo "screenshot: ${TMPDIR:-/tmp}/dyna-e2e-fail.png"; curl -s localhost:17457/stats; echo; exit 1; }

echo "== build"
(cd "$repo/mobile/android" && ./gradlew --no-daemon -q assembleDebug)
(cd "$repo" && go build -o "$work/fakeengine" ./mobile/android/e2e/fakeengine)
"$work/fakeengine" & pids+=($!)

echo "== emulator"
avdmanager list avd | grep -q "Name: dyna_e2e" ||
  echo no | avdmanager create avd -n dyna_e2e -k "$image" -d pixel_6 >/dev/null 2>&1
emulator -avd dyna_e2e -no-window -no-audio -no-boot-anim -no-snapshot -gpu swiftshader_indirect -accel on \
  >"$work/emulator.log" 2>&1 & pids+=($!)
adb wait-for-device
until [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ]; do sleep 2; done
adb shell input keyevent KEYCODE_WAKEUP
adb shell settings put global window_animation_scale 0

echo "== install, pair, join"
adb install -r "$repo/mobile/android/app/build/outputs/apk/debug/app-debug.apk" >/dev/null
adb shell pm clear dev.dyna.call >/dev/null # no saved pairing or permissions from an earlier run
for p in RECORD_AUDIO POST_NOTIFICATIONS; do adb shell pm grant dev.dyna.call android.permission.$p; done
adb shell am start -n dev.dyna.call/.MainActivity >/dev/null
ui() { python3 "$here/ui.py" "$@"; }
ui type 0 "http://10.0.2.2:17457"
ui type 1 "TESTCODE"
ui tap Pair
for _ in $(seq 10); do [ "$(stat pairs)" -gt 0 ] && break; sleep 1; done
[ "$(stat pairs)" -gt 0 ] || fail "the app did not pair"
ui type 0 "dyna" # the pairing form is hidden once paired, so the room field is the first one
ui tap "Join call"
for _ in $(seq 30); do [ "$(stat started)" -gt 0 ] && break; sleep 1; done
[ "$(stat started)" -gt 0 ] || fail "no playback receipt: the app never played Voice"
[ "$(stat mic_frames)" -gt 0 ] || fail "no microphone frames reached the engine"

echo "== home, screen off, wait ${bg}s"
adb shell input keyevent KEYCODE_HOME
sleep 1
adb shell input keyevent KEYCODE_SLEEP
adb shell dumpsys activity services dev.dyna.call | grep -q "isForeground=true" || fail "CallService is not a foreground service"
sleep 2
mic0=$(stat mic_frames); end0=$(stat ended); socks=$(stat sockets)
sleep "$bg"
mic1=$(stat mic_frames); end1=$(stat ended)
echo "mic frames $mic0 -> $mic1, responses ended $end0 -> $end1, sockets $socks -> $(stat sockets)"
[ "$(stat sockets)" = "$socks" ] || fail "the app reconnected: the socket dropped while in the background"
[ "$mic1" -gt $((mic0 + bg * 5)) ] || fail "microphone frames stopped flowing in the background"
[ "$end1" -gt $((end0 + bg / 4)) ] || fail "the phone stopped playing Voice in the background"
echo "PASS: the call stayed up for ${bg}s with the screen off"
