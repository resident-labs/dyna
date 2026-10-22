# Dyna Call (Android)

A thin, call-only Android app. The Svelte web app stays the daily UI. This app pairs
with the engine, joins the live Voice call, and plays its audio. It is sideloaded.

## Build

Needs JDK 17 and the Android SDK (platform 35, build-tools 35.0.0). Set `sdk.dir` in
`local.properties` or `ANDROID_HOME`.

```sh
cd mobile/android
./gradlew testDebugUnitTest assembleDebug
adb install app/build/outputs/apk/debug/app-debug.apk
```

CI compiles the app and runs its JVM tests when `mobile/android` changes (`scripts/ci.sh android`). The emulator scripts in `e2e/` need KVM and stay manual.

## Use

1. Get a pairing code (`dyna engine run --pair`) and type the same address you use in
   the browser (for example `http://100.64.0.1:7457`) with the code. The device gets
   the default scopes, read and operate.
2. Start a call in the web app, type the room name, and press Join call.

The protocol is in `Wire.kt`, copied from `web/src/lib/voice/wire.js`.

## Background

Join call starts `CallService`, a foreground service of type microphone. It holds the
socket, the speaker and the microphone, so Voice keeps going after Home and with the
screen off. It shows one quiet ongoing notification, and holds a partial wake lock and a
Wi-Fi lock. Grant the microphone and notification permissions when asked. Hang up stops it.

Manual check: join a call, press Home, turn the screen off, and talk to Voice two
minutes later.

## Emulator test

`e2e/run.sh [seconds]` starts a headless emulator (needs KVM, `ANDROID_HOME` with the
emulator and the `system-images;android-34;google_apis;x86_64` image, and JDK 17), installs
the APK, pairs it with `e2e/fakeengine` (a fake engine that sends a tone and counts what the
phone sends back), joins, presses Home, turns the screen off, and checks that microphone
frames, playback receipts and the one socket continue for the given time (default 120).

## Updates and the signing key

`build-apk.sh [data dir]` builds the app and puts `dyna-call.apk` and `version.json` in
`<data dir>/android`, where the engine serves them (`/api/android/app.apk`,
`/api/android/version`). `install.sh` runs it on a local-checkout install when `ANDROID_HOME`
and a JDK are set. Every build is signed with one key that the first build creates in the same
folder (`dyna-call.keystore`, with its password beside it, mode 0600). Keep that folder: an
update only installs over the old app when it has the same key, and keeps the pairing.

`version.properties` holds `versionCode`. Raise it only when the app's code or the voice
protocol it speaks (`PROTOCOL` in `Wire.kt`) changes. On launch the app asks the engine for
`version.json` and shows "Update available" only when the number is higher than its own, so an
engine update that does not touch the app never prompts. The button downloads the APK and opens
Android's installer; Android asks to allow installs from this app at that point.

`dyna://pair?url=<web app address>&code=<code>` opens the app and pairs in one step.
