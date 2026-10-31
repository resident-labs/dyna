import assert from "node:assert/strict";
import { test } from "node:test";
import { allowedOrigin, encode, fingerprint, hello, verifyHello } from "./connect.ts";
import { checkPin, finishConnect } from "./connect-flow.ts";

test("a hello binds the fresh nonce, origin and machine to the pinned key", async () => {
  const pair = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
  const key = encode(await crypto.subtle.exportKey("raw", pair.publicKey));
  const original = globalThis.fetch;
  globalThis.fetch = async input => {
    const url = new URL(String(input));
    const nonce = url.searchParams.get("nonce");
    const message = new TextEncoder().encode(`dyna-connect-hello.v1\ninvented-machine\n${url.origin}\n${nonce}`);
    const sig = encode(await crypto.subtle.sign("Ed25519", pair.privateKey, message));
    return Response.json({ machine: "invented-machine", origin: url.origin, nonce, sig, key, relay: "https://relay.example" });
  };
  try {
    const result = await hello("https://machine.example");
    assert.ok(result);
    assert.equal(await verifyHello(result, key), true);
    assert.equal(await verifyHello({ ...result, machine: "other" }, key), false);
    const pin = { machine: result.machine, key, relay: result.relay, at: 1 };
    assert.equal(await checkPin(result, pin), undefined);
    assert.equal((await checkPin({ ...result, relay: "https://other.example" }, pin))?.kind, "stop");
    assert.equal((await checkPin({ ...result, machine: "other" }, pin))?.kind, "stop");
    assert.equal((await checkPin({ ...result, machine: "other" }, undefined))?.kind, "failed");
    assert.equal(await fingerprint(encode(Uint8Array.from({ length: 32 }, (_, index) => index))), "DPPK-GEVG-TUWJ-5U5R");
    assert.equal(allowedOrigin("http://relay.example"), false);
    assert.equal(allowedOrigin("http://127.0.0.1:1234"), true);
    assert.equal(allowedOrigin("https://relay.example/path"), false);
  } finally { globalThis.fetch = original; }
});

test("a return checks its saved machine and key before it contacts the machine", async () => {
  const saved = new Map([["dyna-connect-state", "state"], ["dyna-connect-machine", "machine"], ["dyna-connect-key", "key"], ["dyna-connect-relay", "https://relay.example"]]);
  const oldStorage = Object.getOwnPropertyDescriptor(globalThis, "sessionStorage");
  const oldHistory = Object.getOwnPropertyDescriptor(globalThis, "history");
  let clean = false;
  Object.defineProperty(globalThis, "sessionStorage", { configurable: true, value: { getItem: (key: string) => saved.get(key) ?? null, removeItem: (key: string) => saved.delete(key) } });
  Object.defineProperty(globalThis, "history", { configurable: true, value: { replaceState: () => { clean = true; } } });
  try {
    const result = await finishConnect("#code=invented&mid=machine&mk=wrong&state=state");
    assert.deepEqual(result, { kind: "failed", problem: "state" });
    assert.equal(clean, true);
  } finally {
    if (oldStorage) Object.defineProperty(globalThis, "sessionStorage", oldStorage); else Reflect.deleteProperty(globalThis, "sessionStorage");
    if (oldHistory) Object.defineProperty(globalThis, "history", oldHistory); else Reflect.deleteProperty(globalThis, "history");
  }
});
