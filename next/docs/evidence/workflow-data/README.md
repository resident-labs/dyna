# Workflow post data: local evidence

[Light 1280px expanded capture](light-1280-expanded.png), at native resolution. This actual `PostRow`/`PostData` fixture shows `groups[0].conflicts` opened, unquoted array positions, quoted object keys, quiet small values and capped strings. The browser first proves the 38-item array stays folded when the chip opens. The associated JSON file is not a duplicate feed card. This image replaces the earlier contact sheet; it is evidence, not a production asset.

## Reproduce

With Node 24, run `npm ci` in `next/web`, then start `npx vite --host 0.0.0.0`. Open `/test/workflow-data.html`. The fixture covers long `groups[].conflicts`, objects, scalars, explicit null, hostile strings, Unicode, 299/300/301-byte values, a long single string and an ordinary JSON fence. `?unrelated` includes a different attachment also named `data.json`; `?reference` exercises an oversized transcript reference (the browser script mocks its file response).

Install Playwright outside the checkout, for example `npm install --prefix /tmp/workflow-data-browser playwright`, then run:

```
node test/workflow-data.browser.mjs /tmp/workflow-data-browser/node_modules/playwright/index.mjs /tmp/workflow-data-shots
```

The script uses `/usr/bin/chromium`. It checks light/dark at 1280/390px: no JSON wall or child tree before expansion, native keyboard disclosure, folded long arrays, 50-item paging, safe hostile text, exact byte boundaries, no horizontal page overflow, scroll-to-bottom and no page errors. Focused assertions cover typed associated-file suppression without filtering an unrelated `data.json` or shifting its media-viewer position; one-line small data with Copy only on hover/focus and no Download; unquoted muted array indices versus quoted object keys; and three-line strings with keyboard Show all/Show less. Clipping never changes complete Copy/Download bytes. It also checks lazy oversized-reference loading, a failed fetch and Retry, and a byte-exact 1000-entry download.

## Delivery proof

`go test ./engine -run 'TestWorkflow(PostData|FinalData|HugeTranscript|Data)' -v` covers post and final-result byte boundaries 1023/1024/1025, Unicode bytes, absence/null/array/object/scalar values, preserved text, typed associated-file identity, feed/transcript contracts, forwarding, idempotent retries, JSON MIME reuse, and oversized transcript references. Large-value tests connect the actual recipient `agent.Client.Attach` over a Unix socket to the engine's authenticated `Files` route. They assert the logical `/box/attachments/<seq>/data.json` path and read exact bytes at the corresponding input path in an isolated recipient root, check permissions, and replay the download. The test-only root mapping keeps this proof runnable on CI hosts without a writable `/box`. This is real file/route/client coverage in the test box, **not** a separate-container or remote-host claim. Feed-only filtering does not remove agent files.

Post boundary/value tests failed before implementation (data was lost). The post and final-delivery tests were also run against the spec-only commit, where final JSON remained a wall and no file was delivered. All four final UI policies failed in a browser before their implementation; typed file identity also failed first. Mutation checks separately disable claimed policies, including the recipient's physical file write; outcomes and test counts are recorded in the PR.

**Not checked locally:** separate Podman recipient-container tests (Podman/image harness unavailable), remote-host transport, real screen-reader output, Safari/Firefox. The browser check uses Chromium; its oversized-reference route is mocked. Existing arbitrary text/JSON fences are not migrated or reinterpreted. A JSON-looking final string is text, not structured result data.
