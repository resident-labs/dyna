#!/usr/bin/env bash
# Builds the call app and puts it where the engine serves it:
#   <data dir>/android/dyna-call.apk and version.json ({"versionCode": N, "protocol": P}).
# Every build is signed with one key kept in <data dir>/android (made on the first build), so an
# update installs over the old app and keeps its pairing. Needs JDK 17 and the Android SDK
# (ANDROID_HOME). install.sh runs this on a local-checkout install when both are present.
#
#   mobile/android/build-apk.sh [data dir]     (default: $DYNA_DATA_DIR or ~/.local/share/dyna)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
data=${1:-${DYNA_DATA_DIR:-$HOME/.local/share/dyna}}
out="$data/android"
mkdir -p "$out"
chmod 700 "$out"
keystore="$out/dyna-call.keystore"
passfile="$out/dyna-call.keystore.pass"
if [ ! -f "$keystore" ]; then
  umask 077
  head -c 24 /dev/urandom | base64 | tr -d '/+=\n' > "$passfile"
  keytool -genkeypair -keystore "$keystore" -alias dyna -keyalg RSA -keysize 2048 -validity 36500 \
    -dname "CN=Dyna Call" -storepass "$(cat "$passfile")" -keypass "$(cat "$passfile")" >/dev/null
fi
export DYNA_KEYSTORE="$keystore" DYNA_KEYSTORE_PASS="$(cat "$passfile")"
(cd "$here" && ./gradlew --no-daemon -q assembleDebug)
code=$(sed -n 's/^versionCode=//p' "$here/version.properties")
protocol=$(sed -n 's/^const val PROTOCOL = //p' "$here/app/src/main/kotlin/dev/dyna/call/Wire.kt")
cp "$here/app/build/outputs/apk/debug/app-debug.apk" "$out/dyna-call.apk"
printf '{"versionCode":%s,"protocol":%s}\n' "$code" "$protocol" > "$out/version.json"
echo "built $out/dyna-call.apk (version $code, protocol $protocol)"
