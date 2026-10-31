import { test } from "node:test";
import { strict as assert } from "node:assert";
import { MessageSearch, SessionSearch, type SearchResponse } from "./search.svelte.ts";
import type { SessionSearchResponse } from "./contract.gen.ts";

test("a late search cannot replace the newer query", async () => {
  let resolveOld: (response: SearchResponse) => void = () => {};
  const old = new Promise<SearchResponse>(resolve => { resolveOld = resolve; });
  const search = new MessageSearch(request => request.query === "old" ? old : Promise.resolve({ posts: [], total: 7, names: {} }));
  const first = search.start("old");
  search.prepare("new");
  resolveOld({ posts: [], total: 42, names: {} });
  await first;
  assert.equal(search.snapshot.state, "searching");
  assert.equal(search.snapshot.total, 0);
  await search.start("new");
  assert.equal(search.snapshot.total, 7);
  assert.equal(search.snapshot.state, "done");
});

test("a late session search cannot replace a newer query", async () => {
  let resolveOld: (response: SessionSearchResponse) => void = () => {};
  const old = new Promise<SessionSearchResponse>(resolve => { resolveOld = resolve; });
  const search = new SessionSearch(request => request.query === "old" ? old : Promise.resolve({ sessions: [], total: 61 }));
  const first = search.start("old");
  search.prepare("new");
  resolveOld({ sessions: [], total: 42 });
  await first;
  assert.equal(search.snapshot.total, 0);
  await search.start("new");
  assert.equal(search.snapshot.total, 61);
  assert.equal(search.snapshot.state, "done");
});
