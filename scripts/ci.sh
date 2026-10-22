#!/usr/bin/env bash
# Shared CI commands. Reports live outside the checkout.
set -euo pipefail
cd "$(dirname "$0")/.."
suite=${1:?usage: scripts/ci.sh relay|frontend|go|cross-build|android [phase]}
phase=${2:-all}
export RUNNER_TEMP=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
if [[ $suite == go || $suite == cross-build ]]; then
  source scripts/ci-diagnostics.sh
  ci_diagnostics_begin "$suite" "$phase"
fi
case "$suite" in
  relay)
    cd relay
    npm ci --prefer-offline --no-audit
    npm run typecheck
    npm test -- --reporter=default --reporter=junit --outputFile="${JUNIT_REPORT_PATH:-$RUNNER_TEMP/relay-test.xml}"
    ;;
  frontend)
    # Phases: check (type checks only), test (build, tests, clean tree), all.
    cd web
    npm ci --prefer-offline --no-audit
    if [[ $phase == all || $phase == check ]]; then
      # The four checks do not depend on each other, so they run side by side.
      pids=()
      npm exec -- node scripts/check-typescript.ts & pids+=($!)
      npm exec -- svelte-check --tsconfig ./tsconfig.json & pids+=($!)
      npm exec -- tsc --project ./tsconfig.json & pids+=($!)
      npm exec -- tsc --project ./tsconfig.worker.json & pids+=($!)
      failed=0
      for pid in "${pids[@]}"; do wait "$pid" || failed=1; done
      if ((failed)); then echo "Frontend type check failed" >&2; exit 1; fi
    fi
    if [[ $phase == all || $phase == test ]]; then
      npm run build
      TEST_FILES_PATH="$RUNNER_TEMP/frontend-test-files.jsonl" npm run test:ci
      # A loaded runner makes fast web tests look slow. Re-measure each one alone and
      # keep the shorter time in the report. The local lane has no checker binary.
      if [[ -x "$RUNNER_TEMP/dyna-ci" ]]; then
        "$RUNNER_TEMP/dyna-ci" git ci tests --junit "$JUNIT_REPORT_PATH" --test-files "$RUNNER_TEMP/frontend-test-files.jsonl" --isolate-over 1s --junit-output "$JUNIT_REPORT_PATH"
      fi
      git -c safe.directory="${GITHUB_WORKSPACE:-$PWD/..}" status --short
      test -z "$(git -c safe.directory="${GITHUB_WORKSPACE:-$PWD/..}" status --porcelain)"
    fi
    ;;
  android)
    sdk=${ANDROID_HOME:?set ANDROID_HOME}
    if [[ ! -d "$sdk/platforms/android-35" || ! -d "$sdk/build-tools/35.0.0" ]]; then
      mkdir -p "$sdk/cmdline-tools" "$RUNNER_TEMP/android-tools"
      curl -fsSL -o "$RUNNER_TEMP/android-tools/cmdline-tools.zip" https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip
      unzip -q -o "$RUNNER_TEMP/android-tools/cmdline-tools.zip" -d "$RUNNER_TEMP/android-tools"
      rm -rf "$sdk/cmdline-tools/latest"
      mv "$RUNNER_TEMP/android-tools/cmdline-tools" "$sdk/cmdline-tools/latest"
      # sdkmanager asks for each license; "yes" in a process substitution avoids SIGPIPE under pipefail.
      "$sdk/cmdline-tools/latest/bin/sdkmanager" --sdk_root="$sdk" "platforms;android-35" "build-tools;35.0.0" < <(yes) >/dev/null
    fi
    cd mobile/android
    ./gradlew --no-daemon -q testDebugUnitTest assembleDebug
    ;;
  cross-build)
    started=$SECONDS
    # An optional target (goos/goarch) builds only that one.
    while IFS=/ read -r goos goarch; do
      if [[ -n $phase && $phase != all && $phase != "$goos/$goarch" ]]; then continue; fi
      echo "Building $goos/$goarch"
      CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" ci_diag_go build -buildvcs=false ./...
    done < .github/release-targets
    echo "Cross-build completed in $((SECONDS - started)) seconds." >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
    ;;
  go)
    if [[ "$phase" == all ]]; then
      for step in modules build test vet; do bash scripts/ci.sh go "$step"; done
      if [[ ${CI_DESKTOP:-1} == 1 ]]; then bash scripts/ci.sh go desktop; fi
      bash scripts/ci.sh go installer
      exit 0
    fi
    case "$phase" in
      modules) ci_diag_go mod verify ;;
      build) ci_diag_go build -o "$RUNNER_TEMP/dyna-ci" ./cmd/dyna ;;
      test)
        race=() isolate=()
        if [[ ${CI_EVENT:-push} != pull_request ]]; then race=(-race) isolate=(--isolate-race); fi
        # CI_SHARD=INDEX/COUNT tests one slice of the packages; no value tests all.
        pkgs=(./...)
        if [[ -n ${CI_SHARD:-} ]]; then
          mapfile -t pkgs < <(bash scripts/ci-shard.sh "${CI_SHARD%/*}" "${CI_SHARD#*/}")
          if ((${#pkgs[@]} == 0)); then echo "Shard $CI_SHARD has no packages" >&2; exit 1; fi
        fi
        ci_diag_go test "${race[@]}" -json -timeout 5m "${pkgs[@]}" | tee "$RUNNER_TEMP/go-test.json" | "$RUNNER_TEMP/dyna-ci" git ci tests --isolate-over 1s "${isolate[@]}" --junit-output "$RUNNER_TEMP/go-test.xml"
        ;;
      vet) ci_diag_go vet ./... ;;
      desktop)
        cd desktop
        test -f ../web/dist/index.html
        CGO_ENABLED=0 ci_diag_go vet ./...
        CGO_ENABLED=0 ci_diag_go test -json -timeout 5m ./... | tee "$RUNNER_TEMP/desktop-test.json" | "$RUNNER_TEMP/dyna-ci" git ci tests --junit-output "$RUNNER_TEMP/desktop-test.xml"
        ;;
      installer) shellcheck install.sh ;;
      *) echo "Unknown Go phase: $phase" >&2; exit 2 ;;
    esac
    ;;
  *) echo "Unknown CI suite: $suite" >&2; exit 2 ;;
esac
