import { test } from "node:test";
import { strict as assert } from "node:assert";
import { MessageSearch, type SearchResponse } from "./search.svelte.ts";

test("a late search cannot replace the newer query", async () => {
  let resolveOld: (response: SearchResponse) => void = () => {};
  const old = new Promise<SearchResponse>(resolve => { resolveOld = resolve; });
  const search = new MessageSearch(request => request.query === "old" ? old : Promise.resolve({ posts: [], total: 7 }));
  const first = search.start("old");
  await search.start("new");
  resolveOld({ posts: [], total: 42 });
  await first;
  assert.equal(search.snapshot.total, 7);
  assert.equal(search.snapshot.state, "done");
});
