import { test } from "node:test";
import assert from "node:assert/strict";
import { access, cleanCode, expiring, isFullCode, linkCode, listed, pairAddresses, pairLink, seen, showCode, startingScopes, thisBrowser } from "./devices.ts";
import type { Device } from "./contract.gen.ts";

const device = (id: string, pairedAt: string, more: Partial<Device> = {}): Device =>
  ({ id, name: id, pairedAt, lastSeenAt: pairedAt, expiresAt: pairedAt, scopes: ["read"], current: false, ...more });
const at = Date.parse("2026-10-31T12:00:00Z");

test("a loopback page starts with every scope, and any other page with read and operate", () => {
  assert.equal(startingScopes("dyna.localhost").length, 6);
  assert.equal(startingScopes("127.0.0.1").length, 6);
  assert.deepEqual(startingScopes("studio.example.net"), ["read", "operate"]);
});

test("a public page pairs through itself, and a loopback page only through a route", () => {
  assert.deepEqual(pairAddresses("https://studio.example.net", "studio.example.net", ["https://other.example.net"]), ["https://studio.example.net"]);
  assert.deepEqual(pairAddresses("http://dyna.localhost:7790", "dyna.localhost", ["https://a.example.net", "https://b.example.net"]), ["https://a.example.net", "https://b.example.net"]);
  assert.deepEqual(pairAddresses("http://dyna.localhost:7790", "dyna.localhost", []), []);
});

test("a code reads the same with or without the dash, and only 10 characters are a code", () => {
  assert.equal(cleanCode(" k7qm2-xt9pl "), "K7QM2XT9PL");
  assert.equal(showCode("k7qm2xt9pl"), "K7QM2-XT9PL");
  assert.equal(showCode("k7qm"), "K7QM");
  assert.equal(isFullCode("K7QM2-XT9P"), false);
  assert.equal(isFullCode("K7QM2-XT9PL"), true);
  assert.equal(pairLink("https://studio.example.net", "K7QM2-XT9PL"), "https://studio.example.net/#pair=K7QM2XT9PL");
});

test("a link's code is read from the hash, and a hash without a whole code gives none", () => {
  assert.equal(linkCode("#pair=K7QM2XT9PL"), "K7QM2XT9PL");
  assert.equal(linkCode("#pair=K7QM"), "");
  assert.equal(linkCode(""), "");
});

test("this device comes first, then the newest", () => {
  const list = [device("old", "2026-01-01T00:00:00Z"), device("me", "2025-01-01T00:00:00Z", { current: true }), device("new", "2026-06-01T00:00:00Z")];
  assert.deepEqual(listed(list).map(item => item.id), ["me", "new", "old"]);
});

test("all scopes read as all access, and any fewer are named in list order", () => {
  assert.equal(access(["admin", "computers", "terminal", "files", "operate", "read"]), "All access");
  assert.equal(access(["operate", "read"]), "Read, operate");
  assert.equal(access([]), "No access");
});

test("a row says when its device was last seen, and this device is active now", () => {
  assert.equal(seen(device("a", new Date(at - 12 * 60_000).toISOString()), at), "Seen 12 min ago");
  assert.equal(seen(device("a", new Date(at - 28 * 86_400_000).toISOString()), at), "Seen 28 days ago");
  assert.equal(seen(device("a", new Date(at - 86_400_000).toISOString(), { current: true }), at), "Active now");
});

test("the expiry warning starts at 3 days left", () => {
  const left = (days: number) => expiring(new Date(at + days * 86_400_000).toISOString(), at);
  assert.equal(left(3.5), undefined);
  assert.equal(left(3), "Expires in 3 days");
  assert.equal(left(2), "Expires in 2 days");
  assert.equal(left(0.5), "Expires today");
});

test("the heading names phones and tablets only, and an iPad that reports as a Mac is found by its touch points", () => {
  assert.deepEqual(thisBrowser("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)", 5), { name: "iPhone", named: true });
  assert.deepEqual(thisBrowser("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605", 5), { name: "iPad", named: true });
  assert.deepEqual(thisBrowser("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605", 0), { name: "Safari", named: false });
  assert.deepEqual(thisBrowser("Mozilla/5.0 (X11; Linux x86_64) Chrome/140.0 Safari/537.36", 0), { name: "Chrome", named: false });
});
