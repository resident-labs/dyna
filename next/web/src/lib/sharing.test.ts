import { test } from "node:test";
import assert from "node:assert/strict";
import { connectLine, connectPill, sharingChips, sharingGroup, sharingItems, sharingLine, sharingText, wayName, type SharingItem } from "./sharing.ts";
import { listView } from "./settings-list.ts";
import type { ConnectStatusResponse, Device } from "./contract.gen.ts";

const now = Date.parse("2026-11-04T12:00:00Z");
const day = 86_400_000;
const iso = (offset: number) => new Date(now + offset).toISOString();
const device = (id: string, over: Partial<Device> = {}): Device =>
  ({ id, name: id, pairedAt: iso(-10 * day), lastSeenAt: iso(-2 * day), expiresAt: iso(28 * day), scopes: ["read", "operate"], current: false, ...over });
const status = (over: Partial<ConnectStatusResponse> = {}): ConnectStatusResponse =>
  ({ attempt: { state: "idle", expires: 0 }, linked: false, connectorMissing: false, routes: [], ...over });
const keys = (items: readonly SharingItem[]) => items.map(item => item.key);

test("a way's name comes from its host: loopback is local, .ts.net is Tailscale, anything else is the owner's HTTPS", () => {
  assert.equal(wayName("http://localhost:7790"), "Local address");
  assert.equal(wayName("http://dyna.localhost:7790"), "Local address");
  assert.equal(wayName("http://127.0.0.1:7790"), "Local address");
  assert.equal(wayName("https://hermeslab.tail9c3e.ts.net"), "Tailscale HTTPS");
  assert.equal(wayName("https://HERMESLAB.TAIL9C3E.TS.NET"), "Tailscale HTTPS");
  assert.equal(wayName("http://[::1]:7790"), "Local address");
  assert.equal(wayName("https://foots.net"), "Your HTTPS address");
  assert.equal(wayName("https://ts.net.example.com"), "Your HTTPS address");
  assert.equal(wayName("https://dyna.example.net"), "Your HTTPS address");
});

test("the ways in come first in the engine's order without the tunnel, then Connect, then this device and the newest devices", () => {
  const items = sharingItems(status({ routes: ["https://b.example.net", "http://localhost:7790", "https://t.connect.dev", "https://a.example.net"], tunnel: "https://t.connect.dev", linked: true }), [
    device("old", { pairedAt: iso(-40 * day) }), device("me", { pairedAt: iso(-90 * day), current: true }), device("new", { pairedAt: iso(-1 * day) }),
  ], "http://localhost:7790", now);
  assert.deepEqual(keys(items), ["way:https://b.example.net", "way:http://localhost:7790", "way:https://a.example.net", "connect", "me", "new", "old"]);
  assert.deepEqual(items.map(item => sharingGroup(item).title), ["Ways in", "Ways in", "Ways in", "Ways in", "Devices", "Devices", "Devices"]);
  assert.notEqual(...keys(sharingItems(status({ routes: ["http://d.example.net", "https://d.example.net"] }), [], "", now)).slice(0, 2) as [string, string]);
  // The page came through the local address, so it, and not the tunnel, is this browser's.
  assert.deepEqual(items.filter(item => item.kind !== "device" && item.current).map(item => item.key), ["way:http://localhost:7790"]);
  assert.deepEqual(items.filter(item => item.kind === "way" && item.local).map(item => item.key), ["way:http://localhost:7790"]);
});

test("Connect is this browser's way when the page came through its tunnel, and a status that did not load still lists Connect", () => {
  const through = sharingItems(status({ routes: ["https://t.connect.dev"], tunnel: "https://t.connect.dev", linked: true }), [], "https://t.connect.dev", now);
  assert.deepEqual(keys(through), ["connect"]);
  assert.equal(through[0].kind === "connect" && through[0].current, true);
  const failed = sharingItems(undefined, [device("d")], "http://localhost:7790", now);
  assert.deepEqual(keys(failed), ["connect", "d"]);
  assert.equal(failed[0].kind === "connect" && failed[0].status, undefined);
});

test("a device warns from 3 days left, and the Expiring and Admin chips count devices only", () => {
  const items = sharingItems(status({ routes: ["http://localhost:7790"] }), [
    device("three", { expiresAt: iso(3 * day) }), device("four", { expiresAt: iso(3 * day + 60_000) }), device("today", { expiresAt: iso(day / 2) }),
    device("admin", { scopes: ["read", "admin"] }), device("plain"), device("Admin2", { scopes: ["admin"], expiresAt: iso(2 * day) }),
  ], "http://localhost:7790", now);
  const warning = (id: string) => { const found = items.find(item => item.key === id); return found?.kind === "device" ? found.warning : "none"; };
  assert.equal(warning("three"), "Expires in 3 days");
  assert.equal(warning("four"), undefined);
  assert.equal(warning("today"), "Expires today");
  const view = listView(items, { text: sharingText, key: item => item.key, query: "", chip: sharingChips[0], chips: sharingChips, group: sharingGroup, folded: new Set() });
  assert.deepEqual(view.counts, { all: 8, admin: 2, expiring: 3 });
  const only = (id: string) => listView(items, { text: sharingText, key: item => item.key, query: "", chip: sharingChips.find(chip => chip.id === id)!, chips: sharingChips, group: sharingGroup, folded: new Set() });
  // Ways in hold no scopes and never expire, so a chip leaves their group out.
  assert.deepEqual(only("admin").rows.map(row => row.kind === "group" ? row.group.title : row.key), ["Devices", "admin", "Admin2"]);
  assert.deepEqual(only("expiring").rows.map(row => row.kind === "group" ? row.group.title : row.key).sort(), ["Admin2", "Devices", "three", "today"].sort());
});

test("the search finds a device by name in any case, by host and by access, and a way by its address", () => {
  const items = sharingItems(status({ routes: ["https://hermeslab.tail9c3e.ts.net", "https://t9.relay.example"], tunnel: "https://t9.relay.example", linked: true }), [
    device("iPhone", { host: "omarchy" }), device("Pixel", { scopes: ["read", "terminal"] }), device("Laptop"),
  ], "http://localhost:7790", now);
  const find = (query: string) => listView(items, { text: sharingText, key: item => item.key, query, chip: sharingChips[0], chips: sharingChips, group: sharingGroup, folded: new Set() })
    .rows.flatMap(row => row.kind === "item" ? [row.key] : []);
  assert.deepEqual(find("IPHONE"), ["iPhone"]);
  assert.deepEqual(find("omarchy"), ["iPhone"]);
  assert.deepEqual(find("terminal"), ["Pixel"]);
  assert.deepEqual(find("tail9c3e"), ["way:https://hermeslab.tail9c3e.ts.net"]);
  assert.deepEqual(find("connect"), ["connect"]);
  assert.deepEqual(find("t9.relay"), ["connect"]);
});

test("a row's line: a way's address, a device's seen, access and host", () => {
  const [way, , me, other, hosted] = sharingItems(status({ routes: ["https://dyna.example.net"] }), [
    device("me", { current: true, scopes: ["read", "operate", "files", "terminal", "computers", "admin"], host: "arch", pairedAt: iso(0) }),
    device("other", { pairedAt: iso(-1 * day) }), device("hosted", { host: "omarchy", scopes: ["operate", "read", "files"], lastSeenAt: iso(-12 * 60_000), pairedAt: iso(-2 * day) }),
  ], "http://localhost:7790", now);
  assert.equal(sharingLine(way, now), "https://dyna.example.net");
  assert.equal(sharingLine(me, now), "Active now · All access · on arch");
  assert.equal(sharingLine(other, now), "Seen 2 days ago · Read, operate");
  assert.equal(sharingLine(hosted, now), "Seen 12 min ago · Read, operate, files · on omarchy");
});

test("Connect's line and mark follow its state, turning first, then linked, then waiting", () => {
  assert.equal(connectLine(undefined), "Status did not load");
  assert.equal(connectLine(status()), "Off");
  assert.equal(connectLine(status({ attempt: { state: "failed", expires: 0 } })), "Off");
  assert.equal(connectLine(status({ attempt: { state: "pending", expires: 0 } })), "Waiting for your approval");
  assert.equal(connectLine(status({ attempt: { state: "starting", expires: 0 } })), "Turning on…");
  assert.equal(connectLine(status({ attempt: { state: "unlinking", expires: 0 }, linked: true, tunnel: "https://t.connect.dev" })), "Turning off…");
  assert.equal(connectLine(status({ attempt: { state: "linked", expires: 0 }, linked: true, tunnel: "https://t.connect.dev" })), "https://t.connect.dev");
  assert.equal(connectLine(status({ linked: true })), "No address yet");
  assert.equal(connectPill(status()), undefined);
  assert.equal(connectPill(undefined), undefined);
  assert.deepEqual(connectPill(status({ linked: true })), { label: "On" });
  assert.deepEqual(connectPill(status({ linked: true, connectorMissing: true })), { label: "Connector stopped", tone: "warning" });
  // A stopped connector matters only while Connect is on.
  assert.equal(connectPill(status({ connectorMissing: true })), undefined);
});
