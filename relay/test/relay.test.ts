/// <reference types="@cloudflare/vitest-plugin/types/cloudflare-test" />
import { env, reset, runInDurableObject, SELF } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Directory } from "../src/directory";
import { Passkey } from "./passkey";

const RELAY = "https://connect.test";
const enc = new TextEncoder();
const realFetch = globalThis.fetch;

function b64u(b: ArrayBuffer | Uint8Array): string {
  const u = b instanceof Uint8Array ? b : new Uint8Array(b);
  let s = "";
  for (const c of u) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
function unb64u(s: string): Uint8Array<ArrayBuffer> {
  const p = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  return Uint8Array.from(atob(p), (c) => c.charCodeAt(0));
}
async function sha(s: string): Promise<string> {
  return b64u(await crypto.subtle.digest("SHA-256", enc.encode(s)));
}

// --- a browser with a cookie jar ---

class Browser {
  cookie = "";
  constructor(public passkey: Passkey | null = null) {}
  async post(path: string, body: unknown = {}, headers: Record<string, string> = {}) {
    const res = await SELF.fetch(RELAY + path, {
      method: "POST",
      headers: { Origin: RELAY, "X-RL": "1", "Content-Type": "application/json", Cookie: this.cookie, ...headers },
      body: JSON.stringify(body),
    });
    const set = res.headers.get("Set-Cookie");
    if (set) this.cookie = set.split(";")[0];
    return { status: res.status, body: (await res.json()) as any };
  }
  async ok(path: string, body: unknown = {}) {
    const r = await this.post(path, body);
    if (r.status !== 200) throw new Error(`${path}: ${r.status} ${JSON.stringify(r.body)}`);
    return r.body;
  }
  async enroll(token: string) {
    this.passkey = await Passkey.make("connect.test", RELAY);
    const o = await this.ok("/v1/enroll/options", { token });
    return this.ok("/v1/enroll/verify", { token, id: o.id, response: await this.passkey.create(o.options) });
  }
  async signIn() {
    const o = await this.ok("/v1/auth/options", { purpose: "login" });
    return this.ok("/v1/auth/verify", { id: o.id, purpose: "login", response: await this.passkey!.get(o.options) });
  }
  async stepUp() {
    const o = await this.ok("/v1/auth/options", { purpose: "stepup" });
    return this.ok("/v1/auth/verify", { id: o.id, purpose: "stepup", response: await this.passkey!.get(o.options) });
  }
}

// --- a machine with an Ed25519 key ---

class Machine {
  id = "";
  pub = "";
  secret = "poll-secret-" + Math.random();
  linkId = "";
  private constructor(private kp: CryptoKeyPair) {}
  static async make() {
    const kp = (await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"])) as CryptoKeyPair;
    const m = new Machine(kp);
    m.pub = b64u(await crypto.subtle.exportKey("raw", kp.publicKey));
    return m;
  }
  async sign(data: string): Promise<string> {
    return b64u(await crypto.subtle.sign({ name: "Ed25519" }, this.kp.privateKey, enc.encode(data)));
  }
  async jws(iss: string, path: string, body: string, jti = crypto.randomUUID()) {
    const now = Math.floor(Date.now() / 1000);
    const h = b64u(enc.encode(JSON.stringify({ alg: "EdDSA", typ: "dyna-machine+jws" })));
    const p = b64u(enc.encode(JSON.stringify({ iss, aud: RELAY, htm: "POST", htu: path, iat: now, exp: now + 60, jti, bh: await sha(body) })));
    return `${h}.${p}.${await this.sign(h + "." + p)}`;
  }
  async call(path: string, body: Record<string, unknown>, iss?: string, jti?: string) {
    const text = JSON.stringify(body);
    const res = await SELF.fetch(RELAY + path, {
      method: "POST",
      headers: { Authorization: "DynaMachine " + (await this.jws(iss ?? this.id, path, text, jti)), "Content-Type": "application/json" },
      body: text,
    });
    return { status: res.status, body: (await res.json()) as any };
  }
}

// --- fake Cloudflare API and fake machine tunnel hosts ---

let cfState: { tunnels: Map<string, { name: string; config?: unknown }>; dns: Map<string, any>; calls: string[] };
let machines: Map<string, Machine>;
let mintRequests: any[];
let revokes: any[];
let mintBehavior: "ok" | "bad-sig" | "down";
let failConfigure = 0;

async function relayPub(): Promise<CryptoKey> {
  const j = (await (await SELF.fetch(RELAY + "/.well-known/jwks.json")).json()) as any;
  return crypto.subtle.importKey("jwk", { kty: "OKP", crv: "Ed25519", x: j.keys[0].x }, { name: "Ed25519" }, false, ["verify"]);
}

async function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const req = new Request(input, init);
  const url = new URL(req.url);
  const ok = (result: unknown) => Response.json({ success: true, result, errors: [] });
  if (url.host === "cf.test") {
    expect(req.headers.get("Authorization")).toBe("Bearer test-token");
    const p = url.pathname.replace("/client/v4", "");
    cfState.calls.push(`${req.method} ${p}`);
    let m;
    if (p === "/accounts/acct/cfd_tunnel" && req.method === "GET") {
      const name = url.searchParams.get("name");
      return ok([...cfState.tunnels].filter(([, t]) => t.name === name).map(([id]) => ({ id })));
    }
    if (p === "/accounts/acct/cfd_tunnel" && req.method === "POST") {
      const b = (await req.json()) as any;
      const id = crypto.randomUUID();
      cfState.tunnels.set(id, { name: b.name });
      return ok({ id });
    }
    if ((m = p.match(/^\/accounts\/acct\/cfd_tunnel\/([^/]+)\/configurations$/))) {
      if (failConfigure > 0) {
        failConfigure--;
        return Response.json({ success: false, errors: [{ message: "upstream trouble" }] }, { status: 502 });
      }
      cfState.tunnels.get(m[1])!.config = await req.json();
      return ok({});
    }
    if ((m = p.match(/^\/accounts\/acct\/cfd_tunnel\/([^/]+)\/token$/))) return ok("tunnel-token-" + m[1]);
    if ((m = p.match(/^\/accounts\/acct\/cfd_tunnel\/([^/]+)\/connections$/))) return ok(null);
    if ((m = p.match(/^\/accounts\/acct\/cfd_tunnel\/([^/]+)$/)) && req.method === "DELETE") {
      cfState.tunnels.delete(m[1]);
      return ok(null);
    }
    if (p === "/zones/zone/dns_records" && req.method === "GET") {
      return ok([...cfState.dns.values()].filter((r) => r.name === url.searchParams.get("name")));
    }
    if (p === "/zones/zone/dns_records" && req.method === "POST") {
      const b = (await req.json()) as any;
      const id = crypto.randomUUID();
      cfState.dns.set(id, { ...b, id });
      return ok({ id });
    }
    if ((m = p.match(/^\/zones\/zone\/dns_records\/([^/]+)$/))) {
      if (req.method === "DELETE") cfState.dns.delete(m[1]);
      else cfState.dns.set(m[1], { ...((await req.json()) as any), id: m[1] });
      return ok({});
    }
    return Response.json({ success: false, errors: [{ message: "unknown " + p }] }, { status: 404 });
  }
  const mm = url.host.match(/^m-([a-z0-9]+)\.machines\.test$/);
  if (mm) {
    const jws = await req.text();
    const [h, p, s] = jws.split(".");
    const valid = await crypto.subtle.verify({ name: "Ed25519" }, await relayPub(), unb64u(s), enc.encode(h + "." + p));
    expect(valid).toBe(true);
    const claims = JSON.parse(new TextDecoder().decode(unb64u(p)));
    expect(claims.aud).toBe(mm[1]);
    if (url.pathname === "/connect/v1/revoke") {
      revokes.push(claims);
      return Response.json({ ok: true });
    }
    expect(url.pathname).toBe("/connect/v1/mint");
    mintRequests.push(claims);
    if (mintBehavior === "down") throw new TypeError("network down");
    const machine = machines.get(mm[1])!;
    const code = "code-" + crypto.randomUUID();
    const sig = await machine.sign(`dyna-connect-mint.v1\n${mintBehavior === "bad-sig" ? "wrong" : claims.nonce}\n${await sha(code)}`);
    return Response.json({ code, sig });
  }
  return realFetch(input, init);
}

beforeEach(() => {
  cfState = { tunnels: new Map(), dns: new Map(), calls: [] };
  machines = new Map();
  mintRequests = [];
  revokes = [];
  mintBehavior = "ok";
  failConfigure = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(fakeFetch);
});
afterEach(async () => {
  vi.restoreAllMocks();
  await reset();
});

async function owner(): Promise<Browser> {
  const b = new Browser();
  const r = await b.enroll("bootstrap-token");
  expect(r.user.role).toBe("admin");
  expect(r.recoveryCodes).toHaveLength(10);
  return b;
}

async function invite(admin: Browser, name: string): Promise<Browser> {
  await admin.stepUp();
  const r = await admin.ok("/v1/invites", { name, role: "user" });
  const b = new Browser();
  await b.enroll(r.url.split("#")[1]);
  return b;
}

async function link(b: Browser, name = "desk"): Promise<Machine> {
  const m = await Machine.make();
  const start = await m.call("/v1/link/start", { pubkey: m.pub, pollHash: await sha(m.secret), name, appPort: 7457, cardPort: 7458 }, "link");
  expect(start.status).toBe(200);
  m.linkId = start.body.id;
  expect((await m.call("/v1/link/poll", { id: m.linkId, secret: m.secret }, m.linkId)).body.status).toBe("pending");
  const info = await b.ok("/v1/link/info", { code: start.body.userCode });
  expect(info.fingerprint).toBe(start.body.fingerprint);
  await b.stepUp();
  const ap = await b.ok("/v1/link/approve", { code: start.body.userCode.toLowerCase().replace("-", "") });
  expect(ap.machine.status).toBe("active");
  const poll = await m.call("/v1/link/poll", { id: m.linkId, secret: m.secret }, m.linkId);
  expect(poll.body.status).toBe("linked");
  m.id = poll.body.machine.id;
  machines.set(m.id, m);
  return m;
}

// Enrollment and tunnel provisioning have their own API tests. The open
// tests need an authenticated device and an active machine with a real key.
async function linkedOwner(): Promise<{ b: Browser; m: Machine }> {
  const b = new Browser();
  const token = "mint-device-token";
  b.cookie = "__Host-rl_session=" + token;
  const [hash, m] = await Promise.all([sha(token), Machine.make()]);
  m.id = "mintmachine";
  await runInDurableObject(env.DIRECTORY.get(env.DIRECTORY.idFromName("directory")), (d: Directory) => {
    const now = Date.now();
    d.sql.exec("insert into users (id, name, role, created) values (?, ?, ?, ?)", "mint-owner", "Owner", "admin", now);
    d.createSession(hash, "mint-device", "mint-owner", "Owner browser");
    d.sql.exec(
      "insert into machines (id, user_id, name, pubkey, epoch, status, created, last_seen, generation, app_port, card_port, routes) values (?, ?, ?, ?, 1, 'active', ?, ?, 1, 7457, 7458, '[]')",
      m.id, "mint-owner", "desk", m.pub, now, now,
    );
  });
  machines.set(m.id, m);
  return { b, m };
}

describe("accounts", () => {
  it("bootstraps one admin, then the bootstrap invite is dead", async () => {
    await owner();
    const late = new Browser();
    const r = await late.post("/v1/enroll/options", { token: "bootstrap-token" });
    expect(r.status).toBe(403);
  });

  it("invites work once and make separate accounts", async () => {
    const admin = await owner();
    // Enrollment already creates a fresh stepped-up session. The next test
    // separately checks expiry and the full passkey step-up flow.
    const inv = await admin.ok("/v1/invites", { name: "Sam", role: "user" });
    const token = inv.url.split("#")[1];
    const sam = new Browser();
    const r = await sam.enroll(token);
    expect(r.user.name).toBe("Sam");
    expect(r.user.role).toBe("user");
    const again = new Browser();
    // These checks do not depend on one another and do not change cookies.
    const [reused, me, forbidden] = await Promise.all([
      again.post("/v1/enroll/options", { token }),
      sam.ok("/v1/me"),
      sam.post("/v1/invites", { name: "x" }),
    ]);
    expect(reused.status).toBe(403);
    expect(me.users).toBeUndefined();
    expect(forbidden.status).toBe(403);
  });

  it("needs a passkey step-up for invites, device revoke, link approve and unlink", async () => {
    const admin = await owner();
    // Age the step-up past 5 minutes.
    await runInDurableObject(env.DIRECTORY.get(env.DIRECTORY.idFromName("directory")), (d: any) => {
      d.sql.exec("update sessions set stepped_up_at = 0");
    });
    for (const [path, body] of [
      ["/v1/invites", { name: "Kim" }],
      ["/v1/devices/revoke", { id: "dev_x" }],
      ["/v1/link/approve", { code: "AAAA-AAAA" }],
      ["/v1/machines/unlink", { id: "x" }],
      ["/v1/recovery-codes", {}],
    ] as const) {
      const r = await admin.post(path, body);
      expect(r.status, path).toBe(401);
      expect(r.body.code, path).toBe("step-up");
    }
    await admin.stepUp();
    expect((await admin.post("/v1/invites", { name: "Kim" })).status).toBe(200);
  });

  it("checks Origin, X-RL and the JSON body on every API call", async () => {
    const admin = await owner();
    expect((await admin.post("/v1/me", {}, { Origin: "https://m-evil.machines.test" })).status).toBe(403);
    expect((await admin.post("/v1/me", {}, { "X-RL": "" })).status).toBe(403);
    const res = await SELF.fetch(RELAY + "/v1/me", { method: "POST", headers: { Origin: RELAY, "X-RL": "1", Cookie: admin.cookie, "Content-Type": "text/plain" }, body: "{}" });
    expect(res.status).toBe(415);
    expect(res.headers.get("Access-Control-Allow-Origin")).toBeNull();
  });

  it("signs in with a passkey and signs out", async () => {
    const admin = await owner();
    const b = new Browser(admin.passkey);
    await b.signIn();
    expect((await b.ok("/v1/me")).devices).toHaveLength(2);
    await b.ok("/v1/auth/logout");
    expect((await b.post("/v1/me")).body.code).toBe("sign-in");
  });

  it("a recovery code adds a passkey, revokes devices and bumps machine epochs", async () => {
    const b = new Browser();
    const r = await b.enroll("bootstrap-token");
    const m = await link(b);
    const before = (await m.call("/v1/machine/status", { machine: m.id })).body.machine.epoch;
    const fresh = new Browser();
    fresh.passkey = await Passkey.make("connect.test", RELAY);
    const o = await fresh.ok("/v1/recover/options", { code: r.recoveryCodes[0] });
    await fresh.ok("/v1/recover/verify", { code: r.recoveryCodes[0], id: o.id, response: await fresh.passkey.create(o.options) });
    expect((await b.post("/v1/me")).body.code).toBe("sign-in");
    const after = (await m.call("/v1/machine/status", { machine: m.id })).body.machine.epoch;
    expect(after).toBe(before + 1);
    // The code works once.
    expect((await new Browser().post("/v1/recover/options", { code: r.recoveryCodes[0] })).status).toBe(403);
  });

  it("serves the checked browser source as a script", async () => {
    const page = await SELF.fetch(RELAY + "/recover");
    expect(await page.text()).toContain('src="/app.js"');
    const script = await SELF.fetch(RELAY + "/app.js");
    expect(script.status).toBe(200);
    expect(script.headers.get("Content-Type")).toContain("javascript");
    const source = await script.text();
    expect(source).toContain('document.getElementById("app")');
    expect(source).toContain("function recover()");
    expect(source).not.toContain("export const script");
  });

  it("GET pages have no side effects and a strict CSP", async () => {
    const res = await SELF.fetch(RELAY + "/open?machine=x&origin=https://a&jkt=y&state=z");
    expect(res.status).toBe(200);
    expect(res.headers.get("Content-Security-Policy")).toContain("frame-ancestors 'none'");
    expect(mintRequests).toHaveLength(0);
  });
});

describe("machines", () => {
  it("links a machine and provisions one tunnel and two names", async () => {
    const b = await owner();
    const m = await link(b);
    expect(cfState.tunnels.size).toBe(1);
    const [tid, t] = [...cfState.tunnels][0];
    expect(t.name).toBe("rl-connect-" + m.id);
    expect(JSON.stringify(t.config)).toContain(`"hostname":"m-${m.id}.machines.test","service":"http://127.0.0.1:7457"`);
    expect(JSON.stringify(t.config)).toContain(`"hostname":"mc-${m.id}.machines.test","service":"http://127.0.0.1:7458"`);
    const names = [...cfState.dns.values()].map((r) => `${r.type} ${r.name} ${r.content} ${r.proxied}`).sort();
    expect(names).toEqual([`CNAME m-${m.id}.machines.test ${tid}.cfargotunnel.com true`, `CNAME mc-${m.id}.machines.test ${tid}.cfargotunnel.com true`]);
    const tok = await m.call("/v1/machine/tunnel-token", { machine: m.id });
    expect(tok.body.token).toBe("tunnel-token-" + tid);
  });

  it("keeps the old ports when configuring the tunnel fails, and the next report fixes it", async () => {
    const b = await owner();
    const m = await link(b);
    const [tid] = [...cfState.tunnels.keys()];
    const ingress = () => JSON.stringify(cfState.tunnels.get(tid)!.config);
    failConfigure = 1;
    const bad = await m.call("/v1/machine/origin", { machine: m.id, appPort: 9001, cardPort: 9002 });
    expect(bad.status).toBe(500);
    expect(ingress()).toContain("http://127.0.0.1:7457");
    const retry = await m.call("/v1/machine/origin", { machine: m.id, appPort: 9001, cardPort: 9002 });
    expect(retry.status).toBe(200);
    expect(retry.body.changed).toBe(true);
    expect(ingress()).toContain("http://127.0.0.1:9001");
    expect(ingress()).toContain("http://127.0.0.1:9002");
    // A report with the same ports configures again, so a drifted tunnel heals.
    cfState.tunnels.get(tid)!.config = {};
    const same = await m.call("/v1/machine/origin", { machine: m.id, appPort: 9001, cardPort: 9002 });
    expect(same.body.changed).toBe(false);
    expect(ingress()).toContain("http://127.0.0.1:9001");
  });

  it("refuses a replayed or tampered machine request", async () => {
    const b = await owner();
    const m = await link(b);
    const jti = crypto.randomUUID();
    expect((await m.call("/v1/machine/status", { machine: m.id }, undefined, jti)).status).toBe(200);
    expect((await m.call("/v1/machine/status", { machine: m.id }, undefined, jti)).status).toBe(401);
    const other = await Machine.make();
    const forged = await other.call("/v1/machine/status", { machine: m.id }, m.id);
    expect(forged.status).toBe(401);
  });

  it("will not link one key twice", async () => {
    // Enrollment and tunnel provisioning have separate tests. Seed the
    // existing link so this test checks only duplicate-key rejection.
    const b = new Browser();
    const token = "duplicate-key-token";
    b.cookie = "__Host-rl_session=" + token;
    const [hash, m] = await Promise.all([sha(token), Machine.make()]);
    await runInDurableObject(env.DIRECTORY.get(env.DIRECTORY.idFromName("directory")), (d: Directory) => {
      const now = Date.now();
      d.sql.exec("insert into users (id, name, role, created) values (?, ?, ?, ?)", "owner", "Owner", "admin", now);
      d.createSession(hash, "duplicate-key-device", "owner", "Owner browser");
      d.sql.exec(
        "insert into machines (id, user_id, name, pubkey, epoch, status, created, last_seen, generation, app_port, card_port, routes) values (?, ?, ?, ?, 1, 'active', ?, ?, 1, 7457, 7458, '[]')",
        "linked-machine", "owner", "desk", m.pub, now, now,
      );
    });
    const start = await m.call("/v1/link/start", { pubkey: m.pub, pollHash: await sha("s2"), name: "again", appPort: 1, cardPort: 2 }, "link");
    expect(start.status).toBe(200);
    const r = await b.post("/v1/link/approve", { code: start.body.userCode });
    expect(r.status).toBe(409);
    expect(r.body.error).toBe("this machine key is already linked");
    expect(cfState.calls).toHaveLength(0);
  });

  it("keeps accounts apart", async () => {
    // Enrollment and tunnel provisioning have their own API tests. Seed
    // their results here so this test measures only account isolation.
    const admin = new Browser();
    const sam = new Browser();
    admin.cookie = "__Host-rl_session=admin-isolation-token";
    sam.cookie = "__Host-rl_session=sam-isolation-token";
    const [adminHash, samHash, m, sm] = await Promise.all([
      sha("admin-isolation-token"), sha("sam-isolation-token"), Machine.make(), Machine.make(),
    ]);
    m.id = "adminmachine";
    sm.id = "sammachine";
    await runInDurableObject(env.DIRECTORY.get(env.DIRECTORY.idFromName("directory")), (d: any) => {
      for (const [id, name, role] of [["admin", "Admin", "admin"], ["sam", "Sam", "user"]]) {
        d.sql.exec("insert into users (id, name, role, created) values (?, ?, ?, ?)", id, name, role, Date.now());
      }
      d.createSession(adminHash, "admin-device", "admin", "Admin browser");
      d.createSession(samHash, "sam-device", "sam", "Sam browser");
      for (const [machine, user] of [[m, "admin"], [sm, "sam"]] as const) {
        d.sql.exec(
          "insert into machines (id, user_id, name, pubkey, epoch, status, created, last_seen, generation, app_port, card_port, routes) values (?, ?, ?, ?, 1, 'active', ?, ?, 1, 7457, 7458, '[]')",
          machine.id, user, machine.id, machine.pub, Date.now(), Date.now(),
        );
      }
    });
    const [adminMe, samMe] = await Promise.all([admin.ok("/v1/me"), sam.ok("/v1/me")]);
    expect(adminMe.machines.map((x: any) => x.id)).toEqual([m.id]);
    expect(samMe.machines.map((x: any) => x.id)).toEqual([sm.id]);
    const st = { machine: m.id, origin: `https://m-${m.id}.machines.test`, jkt: "a".repeat(43), state: "s1" };
    expect((await sam.post("/v1/open", st)).status).toBe(404);
    expect((await sam.post("/v1/machines/unlink", { id: m.id })).status).toBe(404);
    // A device of Sam is never a grant on the admin's machine.
    const samDev = samMe.device;
    const g = await m.call("/v1/machine/grant", { machine: m.id, dev: samDev, user: samMe.user.id });
    expect(g.body.active).toBe(false);
  });
});

describe("open and grants", () => {
  it("asks the machine to mint, checks its signature, and redirects to the route", async () => {
    // Enrollment has its own tests; minting must not repeat that setup.
    vi.spyOn(Passkey, "make").mockRejectedValue(new Error("mint fixture must not enroll a passkey"));
    const { b, m } = await linkedOwner();
    const origin = `https://m-${m.id}.machines.test`;
    const r = await b.ok("/v1/open", { machine: m.id, origin, jkt: "k".repeat(43), state: "st8" });
    const u = new URL(r.redirect);
    expect(u.origin).toBe(origin);
    expect(u.pathname).toBe("/connect");
    const f = new URLSearchParams(u.hash.slice(1));
    expect(f.get("code")).toMatch(/^code-/);
    expect(f.get("mk")).toBe(m.pub);
    expect(f.get("mid")).toBe(m.id);
    expect(f.get("state")).toBe("st8");
    expect(f.get("relay")).toBe(RELAY);
    expect(mintRequests).toHaveLength(1);
    expect(cfState.calls).toHaveLength(0);
    const me = await b.ok("/v1/me");
    expect(mintRequests[0]).toMatchObject({ op: "mint", sub: me.user.id, dev: me.device, jkt: "k".repeat(43), origin, aud: m.id });
    expect(mintRequests[0].exp - mintRequests[0].iat).toBe(60);
  });

  it("refuses an origin the machine did not report, a bad signature, and a dead machine", async () => {
    const { b, m } = await linkedOwner();
    const base = { machine: m.id, jkt: "k".repeat(43), state: "s" };
    expect((await b.post("/v1/open", { ...base, origin: "https://evil.example" })).status).toBe(403);
    expect((await b.post("/v1/open", { ...base, origin: "http://192.168.1.4:7457" })).status).toBe(403);
    const routes = await m.call("/v1/machine/routes", { machine: m.id, routes: [{ kind: "tailscale", app: "https://desk.example.ts.net", cards: "https://desk.example.ts.net:8443" }] });
    expect(routes.status).toBe(200);
    expect((await b.post("/v1/open", { ...base, origin: "https://desk.example.ts.net" })).status).toBe(200);
    expect((await m.call("/v1/machine/routes", { machine: m.id, routes: [{ kind: "lan", app: "http://192.168.1.4:7457", cards: "http://192.168.1.4:7458" }] })).status).toBe(400);
    mintBehavior = "bad-sig";
    expect((await b.post("/v1/open", { ...base, origin: `https://m-${m.id}.machines.test` })).body.code).toBe("bad-signature");
    mintBehavior = "down";
    expect((await b.post("/v1/open", { ...base, origin: `https://m-${m.id}.machines.test` })).body.code).toBe("offline");
  });

  it("grants follow device revoke and unlink", async () => {
    const admin = await owner();
    const m = await link(admin);
    // Login has its own test. Seed the second live device so this test
    // exercises grant revocation without another passkey round trip.
    const phone = new Browser();
    phone.cookie = "__Host-rl_session=grant-phone-token";
    const phoneHash = await sha("grant-phone-token");
    await runInDurableObject(env.DIRECTORY.get(env.DIRECTORY.idFromName("directory")), (d: any) => {
      d.createSession(phoneHash, "grant-phone", d.users()[0].id, "Phone");
    });
    const pme = await phone.ok("/v1/me");
    const grant = () => m.call("/v1/machine/grant", { machine: m.id, dev: pme.device, user: pme.user.id });
    expect((await grant()).body).toEqual({ active: true, epoch: 1 });
    // Enrollment already created a fresh stepped-up admin session.
    await admin.ok("/v1/devices/revoke", { id: pme.device });
    expect((await grant()).body.active).toBe(false);
    expect(revokes.some((r) => r.op === "revoke" && r.dev === pme.device)).toBe(true);
    await admin.ok("/v1/machines/unlink", { id: m.id });
    expect((await grant()).status).toBe(410);
    await vi.waitFor(() => expect(cfState.tunnels.size).toBe(0));
    await vi.waitFor(() => expect(cfState.dns.size).toBe(0));
  });

  it("serves the key set", async () => {
    const j = (await (await SELF.fetch(RELAY + "/.well-known/jwks.json")).json()) as any;
    expect(j.keys).toEqual([{ kty: "OKP", crv: "Ed25519", x: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo", kid: "test-1", alg: "EdDSA", use: "sig" }]);
    expect(j.keys[0].d).toBeUndefined();
  });
});
