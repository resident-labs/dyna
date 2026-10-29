#!/usr/bin/env bash
# Shared CI commands. Reports live outside the checkout.
set -euo pipefail
cd "$(dirname "$0")/.."
suite=${1:?usage: scripts/ci.sh relay|frontend|pi|go|cross-build|android [phase]}
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
      # The four checks do not depend on each other, so they run side by side.
      pids=()
      npm exec -- node scripts/check-typescript.ts & pids+=($!)
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
        pkgs=(./...)
        if [[ -n ${CI_SHARD:-} ]]; then
          mapfile -t pkgs < <(bash scripts/ci-shard.sh "${CI_SHARD%/*}" "${CI_SHARD#*/}")
          if ((${#pkgs[@]} == 0)); then echo "Shard $CI_SHARD has no packages" >&2; exit 1; fi
        fi
        # CI_SLICE=I/N runs every Nth top-level test of the one package in the shard,
        # so the slowest package can span shards.
        run=()
        if [[ -n ${CI_SLICE:-} ]]; then
          mapfile -t names < <(go test -list '^Test' "${pkgs[@]}" | grep '^Test' | sort)
          picked=()
          for i in "${!names[@]}"; do
            if ((i % ${CI_SLICE#*/} == ${CI_SLICE%/*})); then picked+=("${names[$i]}"); fi
          done
          run=(-run "^($(IFS='|'; echo "${picked[*]}"))\$")
        fi
        ci_diag_go test -json -timeout 5m "${run[@]}" "${pkgs[@]}" | tee "$RUNNER_TEMP/go-test.json" | "$RUNNER_TEMP/dyna-ci" git ci tests --isolate-over 1s --junit-output "$RUNNER_TEMP/go-test.xml"
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
