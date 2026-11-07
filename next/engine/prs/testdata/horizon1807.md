# Horizon 1807 cancellation fixture

Source: `GET /repos/arismoko/horizon/actions/runs/42393927726/jobs?per_page=100`, read with the GitHub Actions list tool on 2026-11-07. Run attempt 1, PR 1807, head `dcb65e8b60b6b2bcdaf97adb65a25cfe38592adf`.

`horizon1807-jobs-source.json` preserves the complete tool response, before shortening. `horizon1807.json` maps all 11 jobs, in source order, to CheckRun contexts: names unchanged, status and conclusion uppercased for GraphQL. There are no invented or omitted job contexts. Its rollup state FAILURE comes from the incident report; a separate historical GraphQL rollup / suite payload was not retrieved. The source run endpoint confirms completed / cancelled. The suite-free summary test isolates the cancellation bug; suite gating is separately covered by `TestNeedsGatedSuiteSequence` for both PR and main.

Exact job list: changes SUCCESS; nightly-dispatch SKIPPED; prepared-output-check SKIPPED; vanilla-parity SKIPPED; launcher SUCCESS; lint SUCCESS; verify CANCELLED; pool-watch CANCELLED; wasm-bench SKIPPED; ignored-tests SKIPPED; ignored-tests-shards SKIPPED. Every job is COMPLETED.

RED on rewritten main `826bd4d78ec0a2dccf2539a7690761f943442a7d`: the exact fixture returned success and no names. GREEN after the change: cancelled with verify and pool-watch. Command: `go test ./engine/prs -run TestHorizon1807Cancelled -count=1`. Logs and the original source response are retained in `/box/shared/ci-cancelled/`.

The historical head / run payload for Horizon 1721 is unknown. The existing needs-gated sequence test models early success before gated jobs exist; its added cancellation step verifies success plus cancelled still remains pending while the suite is IN_PROGRESS. This is regression coverage of the reported failure mode, not a replay of the unknown 1721 payload.
