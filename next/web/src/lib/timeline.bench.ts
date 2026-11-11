// Run: node --experimental-strip-types src/lib/timeline.bench.ts
import assert from "node:assert/strict";
import { performance } from "node:perf_hooks";
import { deriveTimelineGroups, shownTimelineGroups } from "./timeline.ts";
import type { TranscriptItem } from "./contract.gen.ts";

for (const size of [1000, 10000]) {
  const items: TranscriptItem[] = Array.from({ length: size }, (_, i) => ({
    id: `${i}`, turn: "t", at: "", kind: i % 2 ? "thinking" : "tool",
    posts: i % 100 === 0 ? [{ id: `p${i}`, turn: "t", at: "", kind: "post" }] : undefined,
  }));
  const paths = [() => deriveTimelineGroups(items).filter(group => group.kind !== "work"), () => shownTimelineGroups(items, true)];
  assert.deepEqual(paths[0](), paths[1]());
  for (let i = 0; i < 100; i++) for (const path of paths) path();
  const samples: number[][] = [[], []];
  for (let i = 0; i < 1000; i++) {
    // Alternate order to reduce fixed-order bias.
    for (const index of i % 2 ? [1, 0] : [0, 1]) {
      const start = performance.now();
      paths[index]();
      samples[index].push(performance.now() - start);
    }
  }
  samples.forEach((values, index) => {
    values.sort((a, b) => a - b);
    console.log(JSON.stringify({ size, path: index ? "after" : "before", p50ms: values[500], p95ms: values[950] }));
  });
}
