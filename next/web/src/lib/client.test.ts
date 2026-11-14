import { test } from "node:test";
import assert from "node:assert/strict";
import { renewConnect } from "./connect-watch.ts";
import { recoverEventStream } from "./stream.ts";
import { connectVersion } from "./connect-watch.ts";
import { APIError, blockedBy, call, upload } from "./client.ts";

test("turns a non-JSON error answer into a typed error", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response("<html>Bad gateway</html>", { status: 502 });
  try {
    await assert.rejects(call("room.list", {}), (error: unknown) =>
      error instanceof APIError && error.detail.code === "unavailable");
  } finally { globalThis.fetch = original; }
});

test("a plain-text 403 from the web server means signed out", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response("forbidden\n", { status: 403 });
  try {
    await assert.rejects(call("room.list", {}), (error: unknown) => blockedBy(error) === "signed-out");
  } finally { globalThis.fetch = original; }
});

test("identical reads in flight at once share one request, each with its own copy; writes never share", async () => {
  const original = globalThis.fetch;
  let requests = 0;
  globalThis.fetch = async () => { requests++; await new Promise(resolve => setTimeout(resolve, 10)); return new Response(JSON.stringify({ items: [{ id: "a" }] }), { status: 200 }); };
  try {
    const [a, b] = await Promise.all([call("session.list", { room: "r" }), call("session.list", { room: "r" })]);
    assert.equal(requests, 1);
    assert.notEqual(a, b);
    assert.deepEqual(a, b);
    await call("session.list", { room: "r" });
    assert.equal(requests, 2);
    await Promise.all([call("session.list", { room: "r" }), call("session.list", { room: "other" })]);
    assert.equal(requests, 4);
    await Promise.all([call("session.stop", { session: "s" }), call("session.stop", { session: "s" })]);
    assert.equal(requests, 6);
  } finally { globalThis.fetch = original; }
});

test("Connect refusals map to sign-in states", () => {
  assert.equal(blockedBy(new APIError({ code: "connect-ended", message: "ended" })), "signed-out");
  assert.equal(blockedBy(new APIError({ code: "connect-unavailable", message: "offline" })), "sign-in-unavailable");
});

test("stale API access renews and retries; invalid renewal proof signs out", async () => {
  const original = globalThis.fetch;
  const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
  // Minimal IndexedDB boundary: the browser has already stored its device key.
  Object.defineProperty(globalThis, "indexedDB", { configurable: true, value: { open() {
    const request: any = {};
    request.result = { transaction() {
      const transaction: any = { objectStore: () => ({ get: () => ({ result: pair }) }) };
      setTimeout(() => transaction.oncomplete(), 0);
      return transaction;
    } };
    setTimeout(() => request.onsuccess(), 0);
    return request;
  } } });
  Object.defineProperty(globalThis, "location", { configurable: true, value: { origin: "https://machine.example" } });
  let status = 200, calls = 0, renewals = 0;
  globalThis.fetch = async path => {
    if (path === "/api/connect/renew") { renewals++; return Response.json({}, { status }); }
    calls++;
    return calls % 2 === 1 ? Response.json({ code: "renew", message: "renew" }, { status: 401 }) : Response.json({ rooms: [] });
  };
  try {
    await call("room.list", {});
    assert.equal(calls, 2);
    assert.equal(renewals, 1);
    const [a, b] = await Promise.all([renewConnect(), renewConnect()]);
    assert.equal(a, b);
    assert.equal(renewals, 2);
    status = 403;
    await assert.rejects(call("room.list", {}), error => blockedBy(error) === "signed-out");
  } finally { globalThis.fetch = original; }
});

test("uploads and event stream probes recover stale access", async () => {
  const original = globalThis.fetch;
  let uploads = 0, renewals = 0;
  globalThis.fetch = async path => {
    if (path === "/api/connect/renew") { renewals++; return Response.json({}); }
    if (String(path).startsWith("/v1/files")) {
      return ++uploads === 1 ? Response.json({ code: "renew" }, { status: 401 }) : Response.json({ id: "file" });
    }
    return Response.json({ code: "renew" }, { status: 401 });
  };
  try {
    await upload(new File(["hello"], "hello.txt"), "room");
    assert.equal(uploads, 2);
    assert.equal(renewals, 1);
    assert.equal(await recoverEventStream("/v1/events", connectVersion()), "retry");
    assert.equal(renewals, 2);
  } finally { globalThis.fetch = original; }
});

test("a delayed stale refusal retries using the cookie another call renewed", async () => {
  const original = globalThis.fetch;
  let release!: () => void;
  const delayed = new Promise<void>(resolve => { release = resolve; });
  let started!: () => void;
  const pending = new Promise<void>(resolve => { started = resolve; });
  let rooms = 0, profiles = 0, renewals = 0;
  globalThis.fetch = async path => {
    if (path === "/api/connect/renew") { renewals++; return Response.json({}); }
    if (path === "/v1/profile.list" && ++profiles === 1) {
      started(); await delayed;
      return Response.json({ code: "renew" }, { status: 401 });
    }
    if (path === "/v1/room.list" && ++rooms === 1) return Response.json({ code: "renew" }, { status: 401 });
    return Response.json({});
  };
  try {
    const slow = call("profile.list", {});
    await pending;
    await call("room.list", {});
    release(); await slow;
    assert.equal(renewals, 1);
    assert.equal(profiles, 2);
  } finally { globalThis.fetch = original; }
});

test("background renewal reports outages, waiting and recovery distinctly", async () => {
  const original = globalThis.fetch;
  const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, "window");
  const target = new EventTarget();
  Object.defineProperty(globalThis, "window", { configurable: true, value: target });
  const states: string[] = [];
  target.addEventListener("connect-renewal", event => states.push((event as CustomEvent<string>).detail));
  try {
    for (const status of [503, 429, 200, 403]) {
      globalThis.fetch = async () => Response.json({}, { status });
      await renewConnect();
    }
    globalThis.fetch = async () => { throw new TypeError("offline"); };
    await renewConnect();
    assert.deepEqual(states, ["unavailable", "wait", "ready", "ended", "unavailable"]);
    assert.equal(blockedBy(new APIError({ code: "renew-wait", message: "wait" })), "renew-wait");
  } finally {
    globalThis.fetch = original;
    if (windowDescriptor) Object.defineProperty(globalThis, "window", windowDescriptor);
    else Reflect.deleteProperty(globalThis, "window");
  }
});
