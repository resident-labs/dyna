import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
// Vite's asset URL is not a Node module. Only replace that build-time import.
registerHooks({ resolve(specifier, context, next) {
  if (specifier.endsWith('micWorklet.ts?worker&url')) return { url: 'data:text/javascript,export default "/mic-worklet.js"', shortCircuit: true };
  return next(specifier, context);
} });
const { createPlayer } = await import('./audio.ts');
const { VoiceCall } = await import('./call.ts');
const { silence } = await import('./pcm.ts');

class Source {
  buffer: { getChannelData(channel: number): Float32Array } | null = null;
  onended: (() => void) | null = null;
  stopped = false;
  connect() {}
  start() {}
  stop() { this.stopped = true; }
}
class Context {
  static last: Context;
  currentTime = 0;
  sampleRate = 48000;
  destination = {};
  sources: Source[] = [];
  closed = false;
  audioWorklet = { addModule: async () => {} };
  constructor() { Context.last = this; }
  createBuffer(_channels: number, length: number) { return { getChannelData: () => new Float32Array(length) }; }
  createBufferSource() { const src = new Source(); this.sources.push(src); return src; }
  createMediaStreamSource() { return { connect() {} }; }
  createGain() { return { connect() {} }; }
  async resume() {}
  async close() { this.closed = true; }
}
class Mic {
  static last: Mic;
  port: { onmessage?: (event: { data: Float32Array }) => void } = {};
  constructor() { Mic.last = this; }
  connect() {}
  frame() { this.port.onmessage?.({ data: new Float32Array(1920) }); }
}
class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  binaryType = '';
  sent: (string | ArrayBuffer)[] = [];
  onmessage?: (event: { data: string | ArrayBuffer }) => void;
  onclose?: () => void;
  onerror?: () => void;
  url: URL;
  constructor(url: URL) { this.url = url; Socket.last = this; }
  send(data: string | ArrayBuffer) { this.sent.push(data); }
  close() { this.readyState = 3; this.onclose?.(); }
  event(event: object) { this.onmessage?.({ data: JSON.stringify(event) }); }
  audio() { this.onmessage?.({ data: new ArrayBuffer(48000) }); }
  receipts() { return this.sent.filter(x => typeof x === 'string').map(x => JSON.parse(x as string)); }
}

test('player receipts follow heard audio, not later queued items', () => {
  const ctx = new Context();
  const receipts: [string, string, number][] = [];
  const player = createPlayer(ctx, {}, {
    started: id => receipts.push(['start', id, 0]),
    progress: (id, ms) => receipts.push(['progress', id, ms]),
    ended: (id, ms) => receipts.push(['end', id, ms]),
    cancelled: (id, _reason, ms) => receipts.push(['cancel', id, ms]),
  });
  player.play(silence(24000, 1000), 24000, 'heard');
  player.play(silence(24000, 1000), 24000, 'queued');
  ctx.currentTime = 0.4;
  player.reportPlayed();
  assert.deepEqual(receipts, [['start', 'heard', 0], ['progress', 'heard', 400]]);
  player.stop('barge-in');
  assert.deepEqual(receipts.slice(-2), [['cancel', 'heard', 400], ['cancel', 'queued', 0]]);
  assert.ok(ctx.sources.every(s => s.stopped));
});

test('player reports final cumulative duration after the item drains', () => {
  const ctx = new Context(); let ended: unknown;
  const player = createPlayer(ctx, {}, { started() {}, progress() {}, cancelled() {}, ended: (id, ms) => ended = [id, ms] });
  player.play(silence(24000, 100), 24000, 'one');
  player.play(silence(24000, 200), 24000, 'one');
  player.done('one'); assert.equal(ended, undefined);
  ctx.sources[0].onended?.(); assert.equal(ended, undefined);
  ctx.sources[1].onended?.(); assert.deepEqual(ended, ['one', 300.00000000000006]);
});

test('call start, microphone mute, interruption and stop release resources', async () => {
  let stopped = false;
  const saved = new Map<string, PropertyDescriptor | undefined>();
  const globals = { AudioContext: Context, AudioWorkletNode: Mic, WebSocket: Socket,
    location: { href: 'http://localhost/', protocol: 'http:' },
    navigator: { mediaDevices: { getUserMedia: async () => ({ getTracks: () => [{ stop() { stopped = true; } }] }) } } };
  for (const [key, value] of Object.entries(globals)) { saved.set(key, Object.getOwnPropertyDescriptor(globalThis, key)); Object.defineProperty(globalThis, key, { configurable: true, value }); }
  const call = new VoiceCall(() => {});
  try {
    await call.start('room with spaces'); const ws = Socket.last;
    assert.equal(ws.url.searchParams.get('room'), 'room with spaces');
    Mic.last.frame(); assert.equal(ws.sent.length, 0);
    ws.event({ type: 'ready' }); Mic.last.frame(); assert.equal(ws.sent.length, 1);
    call.mute(); Mic.last.frame(); assert.equal(ws.sent.length, 1);
    call.mute(); Mic.last.frame(); assert.equal(ws.sent.length, 2);
    ws.event({ type: 'transcript', role: 'assistant', item_id: 'heard', text: '' }); ws.audio();
    ws.event({ type: 'transcript', role: 'assistant', item_id: 'queued', text: '' }); ws.audio();
    Context.last.currentTime = 0.25;
    ws.event({ type: 'speech_started' });
    assert.ok(ws.receipts().some(r => r.item_id === 'heard' && r.audio_end_ms === 250));
    assert.ok(ws.receipts().some(r => r.item_id === 'queued' && r.audio_end_ms === 0));
    assert.equal(call.state.status, 'Listening');
    call.stop(); assert.equal(call.state.active, false); assert.equal(ws.readyState, 3);
    assert.ok(stopped && Context.last.closed);
    await call.start("replacement");
    ws.onerror?.(); ws.event({ type: "closed" });
    assert.equal(call.state.active, true);
  } finally {
    call.stop();
    for (const [key, descriptor] of saved) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); }
  }
});

test('player excludes underrun silence from progress and cancellation', () => {
  const ctx = new Context(); const progress: number[] = [];
  const player = createPlayer(ctx, {}, { started() {}, ended() {}, progress: (_id, ms) => progress.push(ms), cancelled: (_id, _reason, ms) => progress.push(ms) });
  player.play(silence(24000, 100), 24000, 'item');
  ctx.sources[0].onended?.(); ctx.currentTime = 1;
  player.play(silence(24000, 100), 24000, 'item');
  player.reportPlayed(); player.stop('interrupted');
  assert.deepEqual(progress, [100, 100]);
});
