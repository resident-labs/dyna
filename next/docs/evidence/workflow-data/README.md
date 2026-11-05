# Workflow post data: local evidence

[Full-size contact sheet](contact-sheet.webp) (all eight screenshots, native 1:1 pixel dimensions; light/dark × 1280/390 × collapsed/expanded). Expanded captures deliberately open `groups[0].conflicts` and its first object. The browser test first proves that the array remains folded when the chip opens. The sheet is evidence, not a production asset.

## Reproduce

With Node 24, run `npm ci` in `next/web`, then start `npx vite --host 0.0.0.0`. Open `/test/workflow-data.html` for the actual `PostRow`/`PostData` fixture. It covers long `groups[].conflicts`, objects, a scalar, explicit null, hostile strings, Unicode, 299/300/301-byte values and an ordinary JSON fence. `/test/workflow-data.html?reference` exercises an oversized transcript reference (the browser script supplies the authenticated file response).

Install Playwright outside the checkout, for example `npm install --prefix /tmp/workflow-data-browser playwright`, then run:

```
node test/workflow-data.browser.mjs /tmp/workflow-data-browser/node_modules/playwright/index.mjs /tmp/workflow-data-shots
```

The script uses `/usr/bin/chromium`; it asserts no JSON wall and no child tree before expansion, native keyboard disclosure, folded long arrays, 50-item paging, safe hostile text, exact boundary behavior, complete clipboard/download values, narrow-layout overflow, scroll-to-bottom, and no page errors. It also exercises lazy oversized-reference loading, a failed fetch and Retry, and a complete 1000-entry download.

## Delivery proof

`go test ./engine -run 'TestWorkflow(PostData|FinalData|HugeTranscript)' -v` covers post and final-result byte boundaries 1023/1024/1025, Unicode bytes, absence/null/array/object/scalar values, preserved text, feed/transcript contracts, forwarding, idempotent retries, and oversized transcript references. Large-value tests connect the actual recipient `agent.Client.Attach` over a Unix socket to the engine's authenticated `Files` route. They read exact bytes at the input's `/box/attachments/<seq>/data.json` path, check permissions, and replay the download. This is real file/route/client coverage in the test box, **not** a separate-container or remote-host claim.

The post boundary/value tests failed before implementation (data was lost). The same post and final-delivery tests were also run against the spec-only commit, where final JSON remained a wall and no file was delivered. Mutation checks separately disable each claimed policy; their outcomes and local test counts are recorded in the PR.

**Not checked locally:** separate Podman recipient-container tests (Podman/image harness unavailable), remote-host transport, real screen-reader output, Safari/Firefox. The browser check uses Chromium. Existing arbitrary text/JSON fences are not migrated or reinterpreted. A JSON-looking final string is text, not structured result data.
