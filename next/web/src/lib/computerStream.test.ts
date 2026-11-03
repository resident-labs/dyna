import test from "node:test";
import assert from "node:assert/strict";
import { autoQuality, setEncodingMode } from "./computerStream.ts";

// v1's Auto tests, with its rules unchanged.
test("the picture starts at quality 6, sharpens after 1.5 seconds still, and needs sustained motion to return", () => {
  const changes: boolean[] = [];
  const q = autoQuality(on => changes.push(on), 0);
  q.sample(0, 1499);
  assert.deepEqual(changes, []);
  q.sample(0, 1400);
  q.sample(0, 1500);
  assert.deepEqual(changes, [false]);
  q.sample(0.2, 1600);
  q.sample(0, 1700);
  q.sample(0, 1901);
  assert.deepEqual(changes, [false], "one changed sample from text refinement does not switch");
  q.sample(0.2, 2000);
  q.sample(0.2, 2100);
  assert.deepEqual(changes, [false, true]);
  q.sample(0, 3599);
  assert.deepEqual(changes, [false, true]);
  q.sample(0, 3600);
  assert.deepEqual(changes, [false, true, false]);
});

test("cursor-sized changes and motion below the threshold do not flap", () => {
  const changes: boolean[] = [];
  const q = autoQuality(on => changes.push(on), 0);
  for (let t = 100; t <= 3000; t += 100) q.sample(0.003, t);
  assert.deepEqual(changes, [false]);
  for (let t = 3100; t <= 5000; t += 100) q.sample(0.2, t);
  assert.deepEqual(changes, [false, true]);
});

test("the sharp redraw cannot at once switch the picture back", () => {
  const changes: boolean[] = [];
  const q = autoQuality(on => changes.push(on), 0);
  q.sample(0, 1400);
  q.sample(0, 1500);
  q.sample(0.2, 1600);
  q.sample(0.2, 1700);
  assert.deepEqual(changes, [false], "ignore the first 300 ms while the sharp picture redraws");
  q.sample(0.2, 1800);
  q.sample(0.2, 1900);
  assert.deepEqual(changes, [false, true], "sustained motion after the redraw still switches");
});

test("a redraw that is still painting holds the sharp picture until it is painted, and scrolling is motion at once", () => {
  const changes: boolean[] = [];
  const q = autoQuality(on => changes.push(on), 0);
  q.sample(0, 1400);
  q.sample(0, 1500);
  q.refreshing();
  for (let t = 1600; t <= 2400; t += 100) q.sample(0.2, t);
  assert.deepEqual(changes, [false]);
  q.refreshed(2400);
  q.sample(0.2, 2800);
  q.sample(0.2, 2900);
  assert.deepEqual(changes, [false, true]);
  q.sample(0, 4500);
  q.sample(0, 4600);
  q.scroll(4700);
  assert.deepEqual(changes, [false, true, false, true]);
});

test("the encoding changes on the open connection, and a sharp picture asks for a full redraw", () => {
  const refresh: unknown[][] = [];
  class FakeRFB {
    static messages = { fbUpdateRequest: (...args: unknown[]) => { refresh.push(args); }, clientEncodings() {} };
    _rfbConnectionState = "connected";
    _sock = {};
    _fbWidth = 1280;
    _fbHeight = 800;
    qualityLevel = 0;
    compressionLevel = 0;
    sent = 0;
    _sendEncodings(): void { this.sent++; }
  }
  const rfb = new FakeRFB();
  setEncodingMode(rfb, true);
  assert.deepEqual([rfb.qualityLevel, rfb.compressionLevel, rfb.sent, refresh.length], [6, 2, 1, 0]);
  setEncodingMode(rfb, false);
  assert.deepEqual([rfb.qualityLevel, rfb.compressionLevel, rfb.sent], [9, 4, 2]);
  assert.deepEqual(refresh.at(-1), [rfb._sock, false, 0, 0, 1280, 800]);
  rfb._rfbConnectionState = "connecting";
  setEncodingMode(rfb, true);
  assert.equal(rfb.sent, 2, "before the connection, the encodings wait for noVNC's own first send");
});
