#!/usr/bin/env bash
# Headless emulator test of the app's update and pairing-link paths: pair with a dyna://pair
# link, find a higher version signed with the same key on the engine, run the install through
# Android's installer (including the "install unknown apps" round trip), and check the app is
# the new version and still paired. Same needs as run.sh.
#
#   ANDROID_HOME=... JAVA_HOME=... mobile/android/e2e/update.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
image="system-images;android-34;google_apis;x86_64"
sdk=${ANDROID_HOME:?set ANDROID_HOME}
export PATH="$JAVA_HOME/bin:$sdk/cmdline-tools/latest/bin:$sdk/platform-tools:$sdk/emulator:$PATH"
export ANDROID_AVD_HOME=${ANDROID_AVD_HOME:-$HOME/.android/avd}
mkdir -p "$ANDROID_AVD_HOME"
work=$(mktemp -d)
pids=()
cleanup() {
  adb emu kill >/dev/null 2>&1 || true
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  pkill -f "qemu-system.*-avd dyna_e2e" 2>/dev/null || true
  git -C "$repo" checkout -- mobile/android/version.properties 2>/dev/null || true
  if [ -z "${KEEP:-}" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT
stat() { curl -fs localhost:17457/stats | python3 -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }
shot() { adb exec-out screencap -p >"${SHOTS:-$work}/$1.png" 2>/dev/null || true; }
fail() { echo "FAIL: $*"; shot fail; curl -s localhost:17457/stats; echo; exit 1; }
ui() { python3 "$here/ui.py" "$@" || fail "ui $*"; }
version() { adb shell dumpsys package dev.dyna.call | sed -n 's/.*versionCode=\([0-9]*\).*/\1/p' | head -1; }

echo "== build the current version and the next one, signed with one key"
old=$(sed -n 's/^versionCode=//p' "$repo/mobile/android/version.properties")
new=$((old + 1))
data="$work/data"
(cd "$repo/mobile/android" && ./build-apk.sh "$data" >/dev/null)
cp "$data/android/dyna-call.apk" "$work/v1.apk"
sed -i "s/^versionCode=.*/versionCode=$new/" "$repo/mobile/android/version.properties"
(cd "$repo/mobile/android" && ./build-apk.sh "$data" >/dev/null)
mkdir -p "$work/serve" && cp "$data/android/dyna-call.apk" "$data/android/version.json" "$work/serve/"
cat "$work/serve/version.json"
(cd "$repo" && go build -o "$work/fakeengine" ./mobile/android/e2e/fakeengine)
"$work/fakeengine" -apk "$work/serve/dyna-call.apk" & pids+=($!)

echo "== emulator"
avdmanager list avd | grep -q "Name: dyna_e2e" ||
  echo no | avdmanager create avd -n dyna_e2e -k "$image" -d pixel_6 >/dev/null 2>&1
emulator -avd dyna_e2e -no-window -no-audio -no-boot-anim -no-snapshot -gpu swiftshader_indirect -accel on \
  >"$work/emulator.log" 2>&1 & pids+=($!)
adb wait-for-device
until [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ]; do sleep 2; done
adb shell input keyevent KEYCODE_WAKEUP

echo "== version 1: pair through a dyna://pair link"
adb uninstall dev.dyna.call >/dev/null 2>&1 || true
adb install "$work/v1.apk" >/dev/null
adb shell am start -a android.intent.action.VIEW -d "'dyna://pair?url=http%3A%2F%2F10.0.2.2%3A17457&code=TESTCODE'" >/dev/null
for _ in $(seq 60); do [ "$(stat pairs)" -gt 0 ] && break; sleep 1; done
[ "$(stat pairs)" -gt 0 ] || fail "the link did not pair the app"
[ "$(version)" = "$old" ] || fail "expected version $old installed, got $(version)"

echo "== update: permission round trip, installer, same key"
ui tap "Update available"
ui wait "Install unknown apps" # Android's own screen, opened by the app
shot permission
ui toggle # the owner's "Allow from this source" switch
adb shell appops get dev.dyna.call REQUEST_INSTALL_PACKAGES | grep -q "allow" || fail "the toggle did not allow installs"
adb shell input keyevent KEYCODE_BACK
ui tap "Update available"
ui wait "Do you want to update this app?" # the package installer
shot installer
ui tap "Update"
for _ in $(seq 60); do [ "$(version)" = "$new" ] && break; sleep 1; done
[ "$(version)" = "$new" ] || fail "the update did not install: version $(version)"
echo "== after the update: still paired"
shot installed
ui tap "Done" # close the installer's "App installed" dialog
adb shell am start -n dev.dyna.call/.MainActivity >/dev/null
ui wait "Not in a call" # the app opens on its main screen
shot after
adb shell run-as dev.dyna.call cat shared_prefs/pairing.xml | grep -q "dyna_device=fake-device-token" || fail "the pairing was lost in the update"
[ "$(stat pairs)" = 1 ] || fail "the app paired again; it should have kept its pairing"
echo "== profile (r13): start-up, frames, heap growth and requests over 20 launches"
heap() { adb shell dumpsys meminfo dev.dyna.call | sed -n 's/^ *Java Heap: *\([0-9]*\).*/\1/p' | head -1; }
adb shell am force-stop dev.dyna.call
adb shell dumpsys gfxinfo dev.dyna.call reset >/dev/null
req0=$(stat requests)
cold=$(adb shell am start -W -n dev.dyna.call/.MainActivity | sed -n 's/^TotalTime: *//p' | tr -d '\r')
ui wait "Not in a call"
heap0=$(heap)
for _ in $(seq 20); do
  adb shell input keyevent KEYCODE_HOME
  adb shell am start -n dev.dyna.call/.MainActivity >/dev/null
done
ui wait "Not in a call"
adb shell dumpsys gfxinfo dev.dyna.call | grep -E "Total frames rendered|Janky frames|90th percentile|99th percentile" | sed 's/^ */  /'
echo "  cold start (am start -W TotalTime): ${cold} ms"
echo "  Java heap: ${heap0} KB after the first launch, $(heap) KB after 20 more (growth $(( $(heap) - heap0 )) KB)"
echo "  requests to the engine: $(( $(stat requests) - req0 ))"
{ adb logcat -d -b crash | grep -c "dev.dyna.call" || true; } | sed 's/^/  crashes logged: /'
echo "PASS: link pairing, same-key update from version $old to $new, pairing kept"
