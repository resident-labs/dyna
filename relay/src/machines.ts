// Machines: linking by device code, managed tunnel provisioning, the
// machine's own signed API, and the broker step that asks a machine to
// mint a one-time code for a browser (/open).
//
// Trust. The machine mints every credential that opens it. The relay only
// asks, with a short signed request, and checks the machine's signed
// answer. A stolen relay signing key could still ask any machine to mint;
// that is the explicit trust the machine places in the relay.

import * as cf from "./cloudflare";
import {
  b64url,
  fingerprint,
  importEd25519Public,
  normalizeUserCode,
  parseJws,
  parseSigningKeys,
  randomId,
  randomToken,
  sha256,
  timingSafeEqual,
  unb64url,
  userCode,
  verifyEd25519,
  verifyJws,
} from "./crypto";
import { LINK_LIFE, type Machine, type Route } from "./directory";
import { managedApp, managedCards, machineUrl, notifyMachine, relaySign } from "./machine-client";
import { browserPost, cleanName, clientIp, dir, HttpError, json, limit, requireSession, requireStepUp, str, type Env } from "./http";

import { mintMessage, type MachineInfo, type MintResponse } from "./protocol";

const enc = new TextEncoder();

// --- the machine's signed requests ---

// verifyMachine checks "Authorization: DynaMachine <JWS>". The JWS is
// signed by the machine key and names the method, path, a hash of the
// body, a 2-minute window and a one-time jti.
async function verifyMachine(req: Request, env: Env, body: string, iss: string, pubkey: string) {
  const h = req.headers.get("Authorization") ?? "";
  if (!h.startsWith("DynaMachine ")) throw new HttpError(401, "missing machine signature");
  let jws;
  try {
    jws = parseJws(h.slice(12));
  } catch {
    throw new HttpError(401, "bad machine signature");
  }
  const p = jws.payload;
  const now = Math.floor(Date.now() / 1000);
  const url = new URL(req.url);
  if (
    p.iss !== iss ||
    p.aud !== env.RELAY_ORIGIN ||
    p.htm !== req.method ||
    p.htu !== url.pathname ||
    typeof p.iat !== "number" ||
    typeof p.exp !== "number" ||
    typeof p.jti !== "string" ||
    p.jti.length < 16 ||
    p.jti.length > 64 ||
    Math.abs(now - p.iat) > 120 ||
    p.exp < now ||
    p.exp - p.iat > 120 ||
    p.bh !== (await sha256(body))
  ) {
    throw new HttpError(401, "the machine signature does not match this request");
  }
  let key;
  try {
    key = await importEd25519Public(unb64url(pubkey));
  } catch {
    throw new HttpError(400, "bad machine key");
  }
  if (!(await verifyJws(jws, key))) throw new HttpError(401, "bad machine signature");
  if (!(await dir(env).useNonce(`m:${iss}:${p.jti}`, (p.exp + 5) * 1000))) throw new HttpError(401, "replayed machine request");
}

async function machineBody(req: Request): Promise<{ text: string; body: Record<string, unknown> }> {
  if (req.method !== "POST") throw new HttpError(405, "use POST");
  const text = await req.text();
  if (text.length > 16 * 1024) throw new HttpError(413, "the body is too large");
  try {
    const body = JSON.parse(text || "{}");
    if (typeof body !== "object" || body === null || Array.isArray(body)) throw new Error();
    return { text, body };
  } catch {
    throw new HttpError(400, "the body must be a JSON object");
  }
}

// signedMachine checks a request from a linked machine and returns it.
async function signedMachine(req: Request, env: Env): Promise<{ m: Machine; body: Record<string, unknown> }> {
  const { text, body } = await machineBody(req);
  const id = str(body.machine, "machine", 20);
  const m = await dir(env).machine(id);
  if (!m) throw new HttpError(404, "this machine is not linked");
  await verifyMachine(req, env, text, id, m.pubkey);
  await limit(env, "machine:" + id, 120, 60_000);
  if (m.status === "unlinked") throw new HttpError(410, "this machine was unlinked", "unlinked");
  await dir(env).touchMachine(id);
  return { m, body };
}

function port(v: unknown, name: string): number {
  if (typeof v !== "number" || !Number.isInteger(v) || v < 1 || v > 65535) throw new HttpError(400, `${name} must be a port number`);
  return v;
}

// --- linking ---

export async function linkStart(req: Request, env: Env): Promise<Response> {
  const { text, body } = await machineBody(req);
  // Link starts are public: cap them per IP.
  await limit(env, "link-start:" + clientIp(req), 10, 3600_000);
  const pubkey = str(body.pubkey, "pubkey", 64);
  if (unb64url(pubkey).length !== 32) throw new HttpError(400, "pubkey must be a raw Ed25519 key");
  await verifyMachine(req, env, text, "link", pubkey);
  const pollHash = str(body.pollHash, "pollHash", 64);
  const name = cleanName(body.name, "name");
  const id = "lnk_" + randomId(12);
  const code = userCode();
  const now = Date.now();
  await dir(env).startLink({
    id,
    userCode: code,
    pubkey,
    pollHash,
    name,
    appPort: port(body.appPort, "appPort"),
    cardPort: port(body.cardPort, "cardPort"),
    created: now,
    expires: now + LINK_LIFE,
  });
  return json({
    id,
    userCode: code,
    url: `${env.RELAY_ORIGIN}/link?code=${code}`,
    fingerprint: await fingerprint(unb64url(pubkey)),
    expires: now + LINK_LIFE,
  });
}

export async function linkPoll(req: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
  const { text, body } = await machineBody(req);
  const id = str(body.id, "id", 40);
  const d = dir(env);
  const l = await d.linkById(id);
  if (!l) throw new HttpError(404, "no such link request");
  await verifyMachine(req, env, text, id, l.pubkey);
  if (!timingSafeEqual(await sha256(str(body.secret, "secret", 100)), l.pollHash)) throw new HttpError(403, "wrong poll secret");
  await limit(env, "link-poll:" + id, 30, 60_000);
  if (l.status === "denied") return json({ status: "denied" });
  if (l.status === "pending") return json({ status: l.expires < Date.now() ? "expired" : "pending", expires: l.expires });
  const m = await d.machine(l.machineId!);
  if (!m || m.status === "unlinked") return json({ status: "denied" });
  if (m.status === "provisioning") {
    if (await d.hit("provision:" + m.id, 1, 30_000)) ctx.waitUntil(provision(env, m).catch((e) => console.error("provision:", e.message)));
    return json({ status: "provisioning" });
  }
  await d.finishLink(l.id);
  const owner = (await d.user(m.userId))!;
  return json({ status: "linked", machine: machineInfo(env, m, owner.name) });
}

function machineInfo(env: Env, m: Machine, ownerName: string): MachineInfo {
  return {
    id: m.id,
    name: m.name,
    owner: { id: m.userId, name: ownerName },
    epoch: m.epoch,
    status: m.status,
    app: managedApp(env, m.id),
    cards: managedCards(env, m.id),
    relay: env.RELAY_ORIGIN,
  };
}

export async function linkInfo(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  await requireSession(req, env);
  const code = normalizeUserCode(str(body.code, "code", 20));
  const l = code ? await dir(env).linkByCode(code) : null;
  if (!l || l.status !== "pending" || l.expires < Date.now()) throw new HttpError(404, "that code is not valid or has expired");
  return json({ name: l.name, code: l.userCode, fingerprint: await fingerprint(unb64url(l.pubkey)), expires: l.expires });
}

export async function linkApprove(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  requireStepUp(a);
  await limit(env, "approve:" + a.user.id, 10, 3600_000);
  const code = normalizeUserCode(str(body.code, "code", 20));
  const d = dir(env);
  const r = await d.approveLink(code, a.user.id, randomId(10));
  if ("error" in r) throw new HttpError(409, r.error);
  try {
    await provision(env, r.machine);
  } catch (e) {
    // The machine keeps polling, and each poll retries provisioning.
    console.error("provision:", e instanceof Error ? e.message : e);
    return json({ machine: { id: r.machine.id, name: r.machine.name, status: "provisioning" } });
  }
  return json({ machine: { id: r.machine.id, name: r.machine.name, status: "active", app: managedApp(env, r.machine.id) } });
}

export async function linkDeny(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  const code = normalizeUserCode(str(body.code, "code", 20));
  if (!(await dir(env).denyLink(code, a.user.id))) throw new HttpError(404, "that code is not valid");
  return json({ ok: true });
}

// provision makes the machine's tunnel and DNS names. Each step can run
// again: it finds the tunnel by name, and records the relay made carry the
// relay's comment. A checkpoint writes only while the generation matches,
// so a late retry stops instead of undoing newer work.
export async function provision(env: Env, m: Machine) {
  const d = dir(env);
  const gen = m.generation;
  let tid = m.tunnelId ?? (await cf.findTunnel(env, m.id));
  if (!tid) tid = await cf.createTunnel(env, m.id);
  if (!(await d.recordTunnel(m.id, gen, tid))) return;
  const app = new URL(managedApp(env, m.id)).host;
  const cards = new URL(managedCards(env, m.id)).host;
  await cf.configureTunnel(env, tid, app, cards, m.appPort, m.cardPort);
  await cf.putRecord(env, m.id, "CNAME", app, `${tid}.cfargotunnel.com`, true);
  await cf.putRecord(env, m.id, "CNAME", cards, `${tid}.cfargotunnel.com`, true);
  await d.markActive(m.id, gen);
}

async function teardown(env: Env, m: Machine) {
  const d = dir(env);
  const fresh = (await d.machine(m.id))!;
  try {
    await cf.deleteRecords(env, m.id, new URL(managedApp(env, m.id)).host);
    await cf.deleteRecords(env, m.id, new URL(managedCards(env, m.id)).host);
    const tid = fresh.tunnelId ?? (await cf.findTunnel(env, m.id));
    if (tid) await cf.deleteTunnel(env, tid);
    await d.setTeardown(m.id, fresh.generation, null);
  } catch (e) {
    await d.setTeardown(m.id, fresh.generation, "failed: " + (e instanceof Error ? e.message : String(e)).slice(0, 200));
    throw e;
  }
}

export async function unlinkByUser(req: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  requireStepUp(a);
  const id = str(body.id, "id", 20);
  const d = dir(env);
  const m = await d.machine(id);
  if (!m || m.userId !== a.user.id) throw new HttpError(404, "no such machine");
  const u = await d.unlink(id, a.session.id);
  if (!u) throw new HttpError(409, "this machine is already unlinked");
  ctx.waitUntil(
    (async () => {
      await notifyMachine(env, m, { op: "revoke", epoch: u.epoch }).catch(() => {});
      await teardown(env, u).catch((e) => console.error("teardown:", e.message));
    })(),
  );
  return json({ ok: true });
}

// --- the machine's own API ---

export async function machineStatus(req: Request, env: Env): Promise<Response> {
  const { m } = await signedMachine(req, env);
  const owner = (await dir(env).user(m.userId))!;
  return json({ machine: machineInfo(env, m, owner.name) });
}

// tunnelToken fetches the connector token from Cloudflare for each call.
// The relay never stores it and never logs it.
export async function machineTunnelToken(req: Request, env: Env): Promise<Response> {
  const { m } = await signedMachine(req, env);
  await limit(env, "tunnel-token:" + m.id, 10, 3600_000);
  if (m.status !== "active" || !m.tunnelId) throw new HttpError(409, "the tunnel is not ready yet");
  return json({ token: await cf.tunnelToken(env, m.tunnelId) });
}

// grant answers: may this relay device still use this machine?
export async function machineGrant(req: Request, env: Env): Promise<Response> {
  const { m, body } = await signedMachine(req, env);
  const dev = str(body.dev, "dev", 40);
  const user = str(body.user, "user", 40);
  const g = await dir(env).grant(m.id, dev);
  return json({ active: g.active && user === m.userId, epoch: g.epoch });
}

function routeOrigin(s: unknown): string {
  if (typeof s !== "string" || s.length > 200) throw new HttpError(400, "bad route origin");
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    throw new HttpError(400, "bad route origin");
  }
  if (u.origin !== s || u.username || u.password) throw new HttpError(400, `${s} is not an origin`);
  const loop = u.hostname === "127.0.0.1" || u.hostname === "localhost" || u.hostname === "[::1]";
  // No route may be plain HTTP except loopback.
  if (u.protocol !== "https:" && !(u.protocol === "http:" && loop)) throw new HttpError(400, `${s}: routes must use HTTPS`);
  return u.origin;
}

export async function machineRoutes(req: Request, env: Env): Promise<Response> {
  const { m, body } = await signedMachine(req, env);
  if (!Array.isArray(body.routes) || body.routes.length > 8) throw new HttpError(400, "routes must be a list of at most 8");
  const routes: Route[] = body.routes.map((r: Record<string, unknown>) => ({
    kind: ["tunnel", "tailscale", "custom", "loopback"].includes(r?.kind as string) ? (r.kind as string) : "custom",
    app: routeOrigin(r?.app),
    cards: routeOrigin(r?.cards),
  }));
  await dir(env).setRoutes(m.id, routes);
  return json({ ok: true });
}

export async function machineOrigin(req: Request, env: Env): Promise<Response> {
  const { m, body } = await signedMachine(req, env);
  const appPort = port(body.appPort, "appPort");
  const cardPort = port(body.cardPort, "cardPort");
  // Configure first, then record, and configure on every report from an
  // active machine. A failed Cloudflare call then leaves the old record,
  // and the machine's next report tries again.
  if (m.tunnelId && m.status === "active") {
    await cf.configureTunnel(env, m.tunnelId, new URL(managedApp(env, m.id)).host, new URL(managedCards(env, m.id)).host, appPort, cardPort);
  }
  if (appPort === m.appPort && cardPort === m.cardPort) return json({ ok: true, changed: false });
  const gen = await dir(env).setOrigin(m.id, appPort, cardPort);
  return json({ ok: true, changed: true, generation: gen });
}

export async function machineUnlink(req: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
  const { m } = await signedMachine(req, env);
  const u = await dir(env).unlink(m.id, "machine:" + m.id);
  if (u) ctx.waitUntil(teardown(env, u).catch((e) => console.error("teardown:", e.message)));
  return json({ ok: true });
}

// --- relay to machine ---


export async function openInfo(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  const m = await dir(env).machine(str(body.machine, "machine", 20));
  if (!m || m.userId !== a.user.id || m.status !== "active") throw new HttpError(404, "this machine is not linked to your account");
  const origin = str(body.origin, "origin", 200);
  return json({ name: m.name, origin, allowed: allowedOrigin(env, m, origin) });
}

function allowedOrigin(env: Env, m: Machine, origin: string): boolean {
  return origin === managedApp(env, m.id) || m.routes.some((r) => r.app === origin);
}

// open is the one click that signs a browser in on a machine. The relay
// asks the machine to mint a one-time code bound to the browser's key,
// checks the machine's signed answer, and sends the browser to the route.
export async function open(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  await limit(env, "open:" + a.user.id, 30, 3600_000);
  const d = dir(env);
  const m = await d.machine(str(body.machine, "machine", 20));
  if (!m || m.userId !== a.user.id || m.status !== "active") throw new HttpError(404, "this machine is not linked to your account");
  const origin = str(body.origin, "origin", 200);
  if (!allowedOrigin(env, m, origin)) throw new HttpError(403, "that address is not a route this machine reported");
  const jkt = str(body.jkt, "jkt", 64);
  if (!/^[A-Za-z0-9_-]{43}$/.test(jkt)) throw new HttpError(400, "jkt must be a key thumbprint");
  const state = str(body.state, "state", 100);
  if (!/^[A-Za-z0-9_-]+$/.test(state)) throw new HttpError(400, "bad state");
  const { jws, nonce } = await relaySign(env, m, { op: "mint", sub: a.user.id, dev: a.session.id, epoch: m.epoch, jkt, origin });
  let res: Response;
  try {
    res = await fetch(machineUrl(env, m, "/connect/v1/mint"), {
      method: "POST",
      headers: { "Content-Type": "application/jose" },
      body: jws,
      redirect: "manual",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    throw new HttpError(502, `${m.name} did not answer through its tunnel; is dyna engine run running there?`, "offline");
  }
  const out = (await res.json().catch(() => null)) as MintResponse | null;
  if (!res.ok || !out?.code || !out.sig) throw new HttpError(502, `${m.name} refused: ${out?.error ?? res.status}`, "refused");
  const key = await importEd25519Public(unb64url(m.pubkey));
  const ok = await verifyEd25519(key, unb64url(out.sig), await mintMessage(nonce, out.code));
  if (!ok) throw new HttpError(502, "the machine's answer was not signed by its key", "bad-signature");
  await d.audit(a.user.id, a.session.id, "machine.open", `${m.name} (${m.id}) at ${origin}`);
  const frag = new URLSearchParams({ code: out.code, state, mid: m.id, mk: m.pubkey, relay: env.RELAY_ORIGIN });
  return json({ redirect: `${origin}/connect#${frag}` });
}

export async function jwks(env: Env): Promise<Response> {
  const keys = await parseSigningKeys(env.RELAY_SIGNING_KEYS);
  return new Response(JSON.stringify({ keys: keys.map((k) => k.publicJwk) }), {
    headers: { "Content-Type": "application/json", "Cache-Control": "public, max-age=300", "X-Content-Type-Options": "nosniff" },
  });
}

export { b64url, enc };
