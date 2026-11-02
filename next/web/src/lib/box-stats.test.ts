import { test, mock } from "node:test";
import assert from "node:assert/strict";
import type { BoxStatsResponse } from "./contract.gen.ts";
import { boxDisk, boxMeters, watchBoxStats } from "./box-stats.ts";

const GB = 2 ** 30, MB = 2 ** 20;
const size: BoxStatsResponse["size"] = { name: "small", cpus: 2, memBytes: 4 * GB, pids: 2048 };

test("a box's meters are its use of its size", () => {
  assert.deepEqual(boxMeters(size, { cpuUsed: 0.4, memUsed: 1.2 * GB }).map(m => [m.label, m.text, m.percent, m.busy]),
    [["CPU", "0.4 of 2 cores", 20, false], ["Memory", "1.2 of 4 GB", 30, false]]);
});

test("a one-core box says core", () => {
  assert.equal(boxMeters({ ...size, cpus: 1 }, { cpuUsed: 0.5, memUsed: GB })[0].text, "0.5 of 1 core");
});

test("memory under a gigabyte reads in MB and never as nothing", () => {
  assert.equal(boxMeters(size, { cpuUsed: 0, memUsed: 340 * MB })[1].text, "340 MB of 4 GB");
  assert.equal(boxMeters(size, { cpuUsed: 0, memUsed: 1024 })[1].text, "1 MB of 4 GB");
});

test("a box over its soft limit stays in the bar and shows busy", () => {
  const memory = boxMeters(size, { cpuUsed: 0, memUsed: 5 * GB })[1];
  assert.equal(memory.percent, 100);
  assert.equal(memory.text, "5.0 of 4 GB");
  assert.equal(memory.busy, true);
  assert.equal(boxMeters(size, { cpuUsed: 1.8, memUsed: GB })[0].busy, true);
  assert.equal(boxMeters(size, { cpuUsed: 1.7, memUsed: GB })[0].busy, false);
});

test("no reading, or no size, gives no meters", () => {
  assert.deepEqual(boxMeters(size, undefined), []);
  assert.deepEqual(boxMeters(size, null), []);
  assert.deepEqual(boxMeters({ ...size, cpus: 0, memBytes: 0 }, { cpuUsed: 1, memUsed: GB }), []);
});

test("disk is a plain figure, and an unmeasured one is left out", () => {
  assert.equal(boxDisk({ cpuUsed: 0, memUsed: 0, diskUsed: 12.4 * GB }), "12.4 GB");
  assert.equal(boxDisk({ cpuUsed: 0, memUsed: 0, diskUsed: 512 * MB }), "512 MB");
  assert.equal(boxDisk({ cpuUsed: 0, memUsed: 0 }), undefined);
  assert.equal(boxDisk(undefined), undefined);
});

// The watch reads document.hidden and its visibilitychange, so these tests supply both.
function page(hidden = false) {
  const listeners = new Set<() => void>();
  const doc = { hidden, addEventListener: (_: string, fn: () => void) => listeners.add(fn), removeEventListener: (_: string, fn: () => void) => listeners.delete(fn) };
  const had = Object.getOwnPropertyDescriptor(globalThis, "document");
  Object.defineProperty(globalThis, "document", { value: doc, configurable: true });
  return { doc, listeners, turn: (next: boolean) => { doc.hidden = next; for (const fn of listeners) fn(); },
    restore: () => { if (had) Object.defineProperty(globalThis, "document", had); else delete (globalThis as { document?: unknown }).document; } };
}
const settled = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };

test("a running box is read every 5 s, one call at a time, until the panel closes", async () => {
  mock.timers.enable({ apis: ["setInterval"] });
  const view = page();
  try {
    let reads = 0, release: () => void = () => {};
    const answers: BoxStatsResponse[] = [];
    const stop = watchBoxStats("s", true, stats => answers.push(stats), () => assert.fail("no failure here"), () => {
      reads++;
      return new Promise(resolve => { release = () => resolve({ size }); });
    });
    assert.equal(reads, 1);
    mock.timers.tick(5_000);
    assert.equal(reads, 1, "a tick waits for the call that is out");
    release();
    await settled();
    assert.equal(answers.length, 1);
    mock.timers.tick(5_000);
    assert.equal(reads, 2);
    stop();
    release();
    await settled();
    assert.equal(answers.length, 1, "an answer after the stop is dropped");
    mock.timers.tick(20_000);
    assert.equal(reads, 2);
    assert.equal(view.listeners.size, 0);
  } finally { view.restore(); mock.timers.reset(); }
});

test("a box that is not running is read once, for its size", async () => {
  mock.timers.enable({ apis: ["setInterval"] });
  const view = page();
  try {
    let reads = 0;
    const stop = watchBoxStats("s", false, () => {}, () => {}, async () => { reads++; return { size }; });
    await settled();
    mock.timers.tick(60_000);
    assert.equal(reads, 1);
    stop();
  } finally { view.restore(); mock.timers.reset(); }
});

test("a hidden page reads nothing, and reads again when it shows", async () => {
  mock.timers.enable({ apis: ["setInterval"] });
  const view = page(true);
  try {
    let reads = 0;
    const stop = watchBoxStats("s", true, () => {}, () => {}, async () => { reads++; return { size }; });
    await settled();
    mock.timers.tick(30_000);
    assert.equal(reads, 0);
    view.turn(false);
    await settled();
    assert.equal(reads, 1);
    mock.timers.tick(5_000);
    assert.equal(reads, 2);
    await settled();
    view.turn(true);
    mock.timers.tick(30_000);
    assert.equal(reads, 2);
    stop();
  } finally { view.restore(); mock.timers.reset(); }
});

test("a failed read tells the panel and leaves the watch running", async () => {
  mock.timers.enable({ apis: ["setInterval"] });
  const view = page();
  try {
    let failures = 0, reads = 0;
    const stop = watchBoxStats("s", true, () => {}, () => failures++, async () => { reads++; throw new Error("gone"); });
    await settled();
    assert.equal(failures, 1);
    mock.timers.tick(5_000);
    await settled();
    assert.equal(reads, 2);
    stop();
  } finally { view.restore(); mock.timers.reset(); }
});
