#!/usr/bin/env bash
# Shared CI commands. Reports live outside the checkout.
set -euo pipefail
cd "$(dirname "$0")/.."
suite=${1:?usage: scripts/ci.sh relay|frontend|pi|go|next|cross-build|android [phase]}
phase=${2:-all}
export RUNNER_TEMP=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
# web/embed.go embeds all:dist, but only the frontend suite builds it. Every
# other Go command needs the directory to exist, so give it one empty,
# git-ignored file. A real frontend build replaces it.
if [[ ! -d web/dist || -z "$(ls -A web/dist 2>/dev/null)" ]]; then
  mkdir -p web/dist
  : > web/dist/.placeholder
fi
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
    # Phases: check (type checks only), build (production build and a clean tree),
    # test (build, tests, clean tree), test-shard (tests of one shard), all.
    # A local CI run gives each test shard its own machine slot, so it runs
    # test-shard with FRONTEND_SHARD set instead of one long test. Without
    # FRONTEND_SHARD the test runner runs every file.
    cd web
    npm ci --prefer-offline --no-audit
    type_checks() {
      # The five checks do not depend on each other, so they run side by side.
      # check-ui-guard reads only the lines this change adds, so it needs the base commit.
      pids=()
      npm exec -- node scripts/check-typescript.ts & pids+=($!)
      npm exec -- node scripts/check-ui-guard.ts & pids+=($!)
      npm exec -- svelte-check --tsconfig ./tsconfig.json & pids+=($!)
      npm exec -- tsc --project ./tsconfig.json & pids+=($!)
      npm exec -- tsc --project ./tsconfig.worker.json & pids+=($!)
      failed=0
      for pid in "${pids[@]}"; do wait "$pid" || failed=1; done
      if ((failed)); then echo "Frontend type check failed" >&2; exit 1; fi
    }
    clean_tree() {
      git -c safe.directory="${GITHUB_WORKSPACE:-$PWD/..}" status --short
      test -z "$(git -c safe.directory="${GITHUB_WORKSPACE:-$PWD/..}" status --porcelain)"
    }
    run_tests() {
      # test:ci hands FRONTEND_SHARD to node --test --test-shard, which splits
      # the test files, so a shard reporting here still runs its whole share.
      TEST_FILES_PATH="$RUNNER_TEMP/frontend-test-files.jsonl" npm run test:ci
      # A loaded runner makes fast web tests look slow. Re-measure each one alone and
      # keep the shorter time in the report. The local lane has no checker binary.
      if [[ -x "$RUNNER_TEMP/dyna-ci" ]]; then
        "$RUNNER_TEMP/dyna-ci" git ci tests --junit "$JUNIT_REPORT_PATH" --test-files "$RUNNER_TEMP/frontend-test-files.jsonl" --isolate-over 1s --junit-output "$JUNIT_REPORT_PATH"
      fi
    }
    case "$phase" in
      check) type_checks ;;
      build) npm run build; clean_tree ;;
      test) npm run build; run_tests; clean_tree ;;
      test-shard) run_tests; clean_tree ;;
      all) type_checks; npm run build; run_tests; clean_tree ;;
      *) echo "Unknown frontend phase: $phase" >&2; exit 2 ;;
    esac
    ;;
  pi)
    # Phases: all (tests and pack), test-shard (PI_SHARD=i/n), pack.
    case "$phase" in
      all|test-shard|pack) ;;
      *) echo "Unknown Pi phase: $phase" >&2; exit 2 ;;
    esac
    started=$SECONDS
    cd third_party/pi
    npm ci --ignore-scripts
    bash ../../scripts/pi-hydrate.sh
    npm run build:offline
    if [[ $phase != pack ]]; then
      PI_OFFLINE= node --import ./packages/coding-agent/src/experimental/source-resolver.ts --input-type=module -e '
        import { ensureTool } from "./packages/coding-agent/src/utils/tools-manager.ts";
        for (const tool of ["fd", "rg"]) {
          const path = await ensureTool(tool, ({ message }) => console.log(message));
          if (!path) throw new Error(`Unable to provision ${tool}`);
        }
      '
      test_args=(--maxWorkers=2 --reporter=default --reporter=junit --outputFile="${JUNIT_REPORT_PATH:-$RUNNER_TEMP/pi-test.xml}")
      if [[ $phase == test-shard ]]; then
        test_args+=(--shard="${PI_SHARD:?test-shard requires PI_SHARD=i/n}")
      fi
      npm test --workspace=@earendil-works/pi-coding-agent -- "${test_args[@]}"
    fi
    if [[ $phase != test-shard ]]; then
      cd ../..
      PI_PACK_SKIP_BUILD=1 bash scripts/pi-pack.sh "$RUNNER_TEMP/pi-fork.tgz"
    fi
    echo "Pi $phase completed in $((SECONDS - started)) seconds." >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
    ;;
  android)
    # TODO: legacy/shim remove later — engines released before the Android app
    # removal still ask for this suite by name. Answer the check and do nothing.
    # Remove once main's local CI no longer lists "android".
    echo "The Android app suite is retired; nothing to run."
    ;;
  next)
    # next/ is a separate Go module with its own go.mod, so the root ./... skips it.
    # The check's golang image matches the go line in next/go.mod.
    # Format runs first and skips next/third_party, which keeps upstream formatting.
    mapfile -d '' files < <(git ls-files -z -- 'next/*.go' ':(exclude)next/third_party')
    if ((${#files[@]})); then
      unformatted=$(gofmt -l "${files[@]}")
      if [[ -n $unformatted ]]; then
        echo "These Go files are not gofmt-formatted; fix them with gofmt -w <files>:" >&2
        echo "$unformatted" >&2
        exit 1
      fi
    fi
    (
      cd next
      go vet ./...
      go test -count=1 -timeout 3m ./...
      # Join the vendored management handler and engine over private RPC.
      go test -count=1 -tags=dyna_integration ./engine/model -run '^TestKimiCompletionBindsAcrossProxyNotification$'
      (cd third_party/cliproxyapi && go test -count=1 ./internal/api/handlers/management -run '^TestDyna(KimiOAuthCompletesAfterAdoption|CompletionRejectsDifferentRuntimeGrant|CompletionRejectsEnabledRuntimeGrant)$')
    )
    (cd cmd/dyna-export && go vet ./... && go test -count=1 ./...)
    ;;
  next-pi)
    cd next/third_party/pi
    export NODE_OPTIONS=--experimental-strip-types
    npm ci --ignore-scripts
    bash scripts/pi-hydrate.sh
    npm run build:offline
    PI_OFFLINE= node --import ./packages/coding-agent/src/experimental/source-resolver.ts --input-type=module -e '
      import { ensureTool } from "./packages/coding-agent/src/utils/tools-manager.ts";
      for (const tool of ["fd", "rg"]) {
        const path = await ensureTool(tool, ({ message }) => console.log(message));
        if (!path) throw new Error(`Unable to provision ${tool}`);
      }
    '
    npm test --workspace=@earendil-works/pi-coding-agent -- --maxWorkers="${PI_TEST_WORKERS:-2}" --reporter=default --reporter=junit --outputFile="${JUNIT_REPORT_PATH:-$RUNNER_TEMP/next-pi-test.xml}"
    npm --prefix packages/ai test -- anthropic-mid-conversation-effort.test.ts
    PI_PACK_SKIP_BUILD=1 bash scripts/pi-pack.sh "$RUNNER_TEMP/next-pi-fork.tgz"
    ;;
  next-web)
    # next/web is the v2 web app, a Svelte project with its own package.json.
    # Its check runs on Node 24, the image set for next-web in local CI.
    # Older Node fails to load the TypeScript configs with "No Svelte configuration found".
    if (($(node -p 'process.versions.node.split(".")[0]') < 24)); then
      echo "next/web needs Node 24 or newer, but node on PATH is $(node -v). Put Node 24 first on PATH." >&2
      exit 1
    fi
    cd next/web
    npm ci --prefer-offline --no-audit
    npm run check
    npm test
    npm run build
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
      for step in modules format build test vet; do bash scripts/ci.sh go "$step"; done
      if [[ ${CI_DESKTOP:-1} == 1 ]]; then bash scripts/ci.sh go desktop; fi
      bash scripts/ci.sh go installer
      exit 0
    fi
    case "$phase" in
      modules) ci_diag_go mod verify ;;
      format) bash scripts/ci-gofmt.sh ;;
      build) ci_diag_go build -o "$RUNNER_TEMP/dyna-ci" ./cmd/dyna ;;
      test)
        # No -race here: pull requests and merge trials run the same flags and share
        # cached results. The nightly workflow runs the race phase below.
        # CI_SHARD=INDEX/COUNT tests one slice of the packages; no value tests all.
        # Each lookup runs in a plain assignment, so a failed lookup stops the phase
        # here. A process substitution would hide the failure and skip the package.
        pkgs=(./...)
        sliced=()
        if [[ -n ${CI_SHARD:-} ]]; then
          whole=$(bash scripts/ci-shard.sh "${CI_SHARD%/*}" "${CI_SHARD#*/}")
          split=$(bash scripts/ci-shard.sh "${CI_SHARD%/*}" "${CI_SHARD#*/}" sliced)
          pkgs=()
          if [[ -n $whole ]]; then mapfile -t pkgs <<<"$whole"; fi
          if [[ -n $split ]]; then mapfile -t sliced <<<"$split"; fi
          if ((${#pkgs[@]} + ${#sliced[@]} == 0)); then echo "Shard $CI_SHARD has no packages" >&2; exit 1; fi
        fi
        # CI_SLICE=I/N runs every Nth top-level test of each sliced package on the
        # shard, so a slow package can span shards. Whole packages run unfiltered.
        slicedPkgs=()
        slicedRuns=()
        for pkg in "${sliced[@]}"; do
          names=$(bash scripts/ci-slice.sh "$pkg" "${CI_SLICE%/*}" "${CI_SLICE#*/}")
          if [[ -z $names ]]; then continue; fi
          slicedPkgs+=("$pkg")
          slicedRuns+=("^($(paste -sd'|' <<<"$names"))\$")
        done
        {
          if ((${#pkgs[@]})); then ci_diag_go test -json -timeout 5m "${pkgs[@]}"; fi
          for i in "${!slicedPkgs[@]}"; do
            ci_diag_go test -json -timeout 5m -run "${slicedRuns[$i]}" "${slicedPkgs[$i]}"
          done
        } | tee "$RUNNER_TEMP/go-test.json" | "$RUNNER_TEMP/dyna-ci" git ci tests --isolate-over 1s --junit-output "$RUNNER_TEMP/go-test.xml"
        ;;
      # The nightly race run on main. It has no time budget and no checker.
      race) ci_diag_go test -race -timeout 10m ./... ;;
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
