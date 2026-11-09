import test from "node:test";
import assert from "node:assert/strict";
import { transcriptReader } from "./transcript-reader.ts";

test("leaving a thread stops its chunk chain after the in-flight response", async () => {
  const original = globalThis.fetch;
  try {
    for (const leave of [false, true]) {
      let active = true, requests = 0;
      globalThis.fetch = async () => {
        requests++;
        if (leave) active = false;
        return Response.json({ chunk: "x".repeat(64 << 10), ...(requests < 256 ? { nextOffset: requests * (64 << 10) } : {}) });
      };
      const read = transcriptReader(() => active);
      let offset: number | undefined = 0;
      do {
        const page = await read({ session: "long", turn: "t", item: "message", part: "text", offset });
        if (!page) break;
        offset = page.nextOffset;
      } while (offset !== undefined);
      assert.equal(requests, leave ? 1 : 256);
      if (leave) assert.equal(await read({ session: "long" }), undefined);
      assert.equal(requests, leave ? 1 : 256);
    }
  } finally { globalThis.fetch = original; }
});

test("disposal does not cancel a shared response needed by another thread", async () => {
  const original = globalThis.fetch;
  let finish!: (response: Response) => void;
  let requests = 0, active = true;
  globalThis.fetch = () => { requests++; return new Promise<Response>(resolve => { finish = resolve; }); };
  try {
    const old = transcriptReader(() => active);
    const current = transcriptReader(() => true);
    const request = { session: "s", turn: "t" };
    const first = old(request), second = current(request);
    active = false;
    finish(Response.json({ chunk: "shared" }));
    assert.equal(await first, undefined);
    assert.equal((await second)?.chunk, "shared");
    assert.equal(requests, 1);
  } finally { globalThis.fetch = original; }
});
