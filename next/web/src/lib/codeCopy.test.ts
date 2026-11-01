import { test } from "node:test";
import assert from "node:assert/strict";
import { withCopyButtons } from "./codeCopy.ts";

test("each code block gets its own copy button in front of it", () => {
  assert.equal(withCopyButtons("<pre><code>npm test</code></pre><p>Then:</p><pre><code>go test ./...</code></pre>"),
    '<div class="code-block"><div class="code-head"><button class="code-copy" type="button">Copy code</button></div><pre><code>npm test</code></pre></div>' +
    '<p>Then:</p>' +
    '<div class="code-block"><div class="code-head"><button class="code-copy" type="button">Copy code</button></div><pre><code>go test ./...</code></pre></div>');
});
