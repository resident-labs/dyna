// Accounts and relay devices. Every sign-in is a passkey (WebAuthn with
// user verification). New accounts come only from a one-time invite;
// lost passkeys come back only through a recovery code or an admin's
// recovery invite. The session cookie names one relay device.

import {
  generateAuthenticationOptions,
  generateRegistrationOptions,
  verifyAuthenticationResponse,
  verifyRegistrationResponse,
  type AuthenticationResponseJSON,
  type RegistrationResponseJSON,
} from "@simplewebauthn/server";
import { b64url, randomId, randomToken, sha256, unb64url } from "./crypto";
import { INVITE_LIFE, SESSION_LIFE, type Credential, type Role, type User } from "./directory";
import {
  browserPost,
  cleanName,
  clientIp,
  dir,
  HttpError,
  json,
  limit,
  requireSession,
  requireStepUp,
  sessionCookie,
  str,
  type Auth,
  type Env,
} from "./http";

import { managedApp, notifyMachine } from "./machine-client";

const enc = new TextEncoder();

function regOptions(env: Env, user: { id: string; name: string }, exclude: Credential[]) {
  return generateRegistrationOptions({
    rpName: env.RP_NAME,
    rpID: env.RP_ID,
    userName: user.name,
    userDisplayName: user.name,
    userID: new Uint8Array(enc.encode(user.id)),
    attestationType: "none",
    authenticatorSelection: { residentKey: "required", userVerification: "required" },
    excludeCredentials: exclude.map((c) => ({ id: c.id })),
  });
}

async function verifyReg(env: Env, challenge: string, response: RegistrationResponseJSON): Promise<Omit<Credential, "userId">> {
  let v;
  try {
    v = await verifyRegistrationResponse({
      response,
      expectedChallenge: challenge,
      expectedOrigin: env.RELAY_ORIGIN,
      expectedRPID: env.RP_ID,
      requireUserVerification: true,
    });
  } catch (e) {
    throw new HttpError(400, "the passkey could not be checked: " + (e instanceof Error ? e.message : "unknown"));
  }
  if (!v.verified) throw new HttpError(400, "the passkey was not accepted");
  const c = v.registrationInfo.credential;
  const now = Date.now();
  return {
    id: c.id,
    publicKey: b64url(c.publicKey),
    counter: c.counter,
    transports: (c.transports ?? []).join(","),
    name: "passkey",
    created: now,
    lastUsed: now,
  };
}

async function newSession(env: Env, userId: string, deviceName: string): Promise<{ cookie: string; id: string }> {
  const tok = randomToken();
  const id = "dev_" + randomId(12);
  await dir(env).createSession(await sha256(tok), id, userId, deviceName);
  return { cookie: sessionCookie(env, tok, SESSION_LIFE / 1000), id };
}

function deviceName(req: Request, body: Record<string, unknown>): string {
  if (typeof body.deviceName === "string" && body.deviceName.trim()) return cleanName(body.deviceName, "device name");
  const ua = req.headers.get("User-Agent") ?? "";
  const os = /iPhone|iPad/.test(ua) ? "iPhone" : /Android/.test(ua) ? "Android" : /Mac OS/.test(ua) ? "Mac" : /Linux/.test(ua) ? "Linux" : /Windows/.test(ua) ? "Windows" : "browser";
  const br = /Firefox\//.test(ua) ? "Firefox" : /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "";
  return (br ? br + " on " : "") + os;
}

async function recoveryCodes(env: Env, userId: string): Promise<string[]> {
  const codes = Array.from({ length: 10 }, () => randomToken(10));
  await dir(env).setRecoveryCodes(userId, await Promise.all(codes.map((c) => sha256("recovery:" + c))));
  return codes;
}

// --- enroll with an invite ---

async function inviteFor(env: Env, token: string): Promise<{ hash: string; name: string; role: Role; kind: string; userId: string | null; bootstrap: boolean }> {
  const hash = await sha256("invite:" + token);
  const d = dir(env);
  const inv = await d.peekInvite(hash);
  if (inv) return { hash, name: inv.name, role: inv.role, kind: inv.kind, userId: inv.userId, bootstrap: false };
  // The bootstrap invite makes the first admin. The Directory refuses it
  // once any account exists, so it is not a standing backdoor.
  if (env.BOOTSTRAP_INVITE_HASH && (await sha256(token)) === env.BOOTSTRAP_INVITE_HASH && (await d.userCount()) === 0) {
    return { hash: "bootstrap", name: env.BOOTSTRAP_NAME || "Owner", role: "admin", kind: "enroll", userId: null, bootstrap: true };
  }
  throw new HttpError(403, "this invite is not valid; it may be used or expired");
}

export async function enrollOptions(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  await limit(env, "invite:" + clientIp(req), 10, 3600_000);
  const inv = await inviteFor(env, str(body.token, "token"));
  const d = dir(env);
  const userId = inv.userId ?? "usr_" + randomId(12);
  const exclude = inv.userId ? await d.credentials(inv.userId) : [];
  const opts = await regOptions(env, { id: userId, name: inv.name }, exclude);
  const cid = randomToken(16);
  await d.putChallenge(cid, "enroll", opts.challenge, userId, JSON.stringify({ hash: inv.hash }));
  return json({ id: cid, options: opts, invite: { name: inv.name, role: inv.role, kind: inv.kind } });
}

export async function enrollVerify(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  await limit(env, "invite:" + clientIp(req), 10, 3600_000);
  const d = dir(env);
  const ch = await d.takeChallenge(str(body.id, "id"), "enroll");
  if (!ch) throw new HttpError(400, "the sign-up step expired; start again");
  const token = str(body.token, "token");
  const inv = await inviteFor(env, token);
  if (JSON.parse(ch.data).hash !== inv.hash) throw new HttpError(400, "the invite changed; start again");
  const cred = await verifyReg(env, ch.challenge, body.response as RegistrationResponseJSON);
  let user: User | null;
  if (inv.bootstrap) user = await d.redeemBootstrap(cred, ch.userId!, inv.name);
  else user = await d.redeemInvite(inv.hash, cred, ch.userId!);
  if (!user) throw new HttpError(403, "this invite is not valid; it may be used or expired");
  const s = await newSession(env, user.id, deviceName(req, body));
  // A new account gets recovery codes once; a recovery invite keeps the
  // account's codes.
  const codes = inv.kind === "enroll" ? await recoveryCodes(env, user.id) : [];
  return json({ user, recoveryCodes: codes }, 200, { "Set-Cookie": s.cookie });
}

// --- recover with a code ---

export async function recoverOptions(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  // Recovery codes are single-factor secrets: limit harder than sign-in.
  await limit(env, "recover:" + clientIp(req), 5, 3600_000);
  const code = str(body.code, "code", 40).trim();
  const d = dir(env);
  const userId = await d.recoveryCodeUser(await sha256("recovery:" + code));
  if (!userId) throw new HttpError(403, "that recovery code is not valid");
  const user = (await d.user(userId))!;
  const opts = await regOptions(env, user, await d.credentials(userId));
  const cid = randomToken(16);
  await d.putChallenge(cid, "recover", opts.challenge, userId, "");
  return json({ id: cid, options: opts });
}

export async function recoverVerify(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  await limit(env, "recover:" + clientIp(req), 5, 3600_000);
  const d = dir(env);
  const ch = await d.takeChallenge(str(body.id, "id"), "recover");
  if (!ch) throw new HttpError(400, "the recovery step expired; start again");
  const cred = await verifyReg(env, ch.challenge, body.response as RegistrationResponseJSON);
  const user = await d.useRecoveryCode(await sha256("recovery:" + str(body.code, "code", 40).trim()), { ...cred, name: "passkey (recovered)" });
  if (!user || user.id !== ch.userId) throw new HttpError(403, "that recovery code is not valid");
  const s = await newSession(env, user.id, deviceName(req, body));
  await revokeAllOnMachines(env, user.id);
  return json({ user }, 200, { "Set-Cookie": s.cookie });
}

// --- add a passkey to a signed-in account ---

export async function passkeyOptions(req: Request, env: Env): Promise<Response> {
  await browserPost(req, env);
  const a = await requireSession(req, env);
  requireStepUp(a);
  const d = dir(env);
  const opts = await regOptions(env, a.user, await d.credentials(a.user.id));
  const cid = randomToken(16);
  await d.putChallenge(cid, "passkey", opts.challenge, a.user.id, "");
  return json({ id: cid, options: opts });
}

export async function passkeyVerify(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  requireStepUp(a);
  const d = dir(env);
  const ch = await d.takeChallenge(str(body.id, "id"), "passkey");
  if (!ch || ch.userId !== a.user.id) throw new HttpError(400, "the passkey step expired; start again");
  const cred = await verifyReg(env, ch.challenge, body.response as RegistrationResponseJSON);
  const name = typeof body.name === "string" && body.name.trim() ? cleanName(body.name, "name") : "passkey";
  await d.addCredential({ ...cred, name, userId: a.user.id });
  await d.audit(a.user.id, a.session.id, "passkey.add", name);
  return json({ ok: true });
}

// --- sign in and step up ---

export async function authOptions(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  await limit(env, "auth:" + clientIp(req), 30, 600_000);
  const purpose = body.purpose === "stepup" ? "stepup" : "login";
  const d = dir(env);
  let allow: { id: string }[] = [];
  let userId: string | null = null;
  if (purpose === "stepup") {
    const a = await requireSession(req, env);
    userId = a.user.id;
    allow = (await d.credentials(userId)).map((c) => ({ id: c.id }));
  }
  const opts = await generateAuthenticationOptions({ rpID: env.RP_ID, userVerification: "required", allowCredentials: allow });
  const cid = randomToken(16);
  await d.putChallenge(cid, purpose, opts.challenge, userId, "");
  return json({ id: cid, options: opts });
}

export async function authVerify(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  await limit(env, "auth:" + clientIp(req), 30, 600_000);
  const purpose = body.purpose === "stepup" ? "stepup" : "login";
  const d = dir(env);
  const ch = await d.takeChallenge(str(body.id, "id"), purpose);
  if (!ch) throw new HttpError(400, "the sign-in step expired; start again");
  const response = body.response as AuthenticationResponseJSON;
  const cred = await d.credential(str(response?.id, "credential id", 1024));
  if (!cred) throw new HttpError(403, "this passkey is not known here");
  let a: Auth | null = null;
  if (purpose === "stepup") {
    a = await requireSession(req, env);
    if (cred.userId !== a.user.id || ch.userId !== a.user.id) throw new HttpError(403, "that passkey belongs to another account");
  }
  let v;
  try {
    v = await verifyAuthenticationResponse({
      response,
      expectedChallenge: ch.challenge,
      expectedOrigin: env.RELAY_ORIGIN,
      expectedRPID: env.RP_ID,
      requireUserVerification: true,
      credential: { id: cred.id, publicKey: unb64url(cred.publicKey), counter: cred.counter, transports: [] },
    });
  } catch (e) {
    throw new HttpError(403, "the passkey could not be checked: " + (e instanceof Error ? e.message : "unknown"));
  }
  if (!v.verified) throw new HttpError(403, "the passkey was not accepted");
  await d.touchCredential(cred.id, v.authenticationInfo.newCounter);
  if (a) {
    await d.stepUp(a.tokenHash);
    return json({ ok: true });
  }
  const s = await newSession(env, cred.userId, deviceName(req, body));
  return json({ user: await d.user(cred.userId) }, 200, { "Set-Cookie": s.cookie });
}

export async function logout(req: Request, env: Env): Promise<Response> {
  await browserPost(req, env);
  const a = await requireSession(req, env);
  await dir(env).endSession(a.tokenHash);
  return json({ ok: true }, 200, { "Set-Cookie": sessionCookie(env, "", 0) });
}

// --- account page data and actions ---

export async function me(req: Request, env: Env): Promise<Response> {
  await browserPost(req, env);
  const a = await requireSession(req, env);
  const d = dir(env);
  const machines = (await d.machines(a.user.id)).map((m) => ({
    id: m.id,
    name: m.name,
    status: m.status,
    created: m.created,
    lastSeen: m.lastSeen,
    app: managedApp(env, m.id),
  }));
  const devices = (await d.sessions(a.user.id)).map((s) => ({ ...s, current: s.id === a.session.id }));
  const passkeys = (await d.credentials(a.user.id)).map((c) => ({ id: c.id.slice(0, 12), name: c.name, created: c.created, lastUsed: c.lastUsed }));
  const out: Record<string, unknown> = {
    user: a.user,
    device: a.session.id,
    machines,
    devices,
    passkeys,
    recoveryCodesLeft: await d.recoveryCodesLeft(a.user.id),
    audit: await d.auditFor(a.user.id, 40),
  };
  if (a.user.role === "admin") out.users = await d.users();
  return json(out);
}

export async function revokeDevice(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  requireStepUp(a);
  const id = str(body.id, "id", 40);
  if (!(await dir(env).revokeSession(a.user.id, id, a.session.id))) throw new HttpError(404, "no such device");
  await revokeAllOnMachines(env, a.user.id, id);
  return json({ ok: true });
}

export async function newRecoveryCodes(req: Request, env: Env): Promise<Response> {
  await browserPost(req, env);
  const a = await requireSession(req, env);
  requireStepUp(a);
  return json({ recoveryCodes: await recoveryCodes(env, a.user.id) });
}

export async function createInvite(req: Request, env: Env): Promise<Response> {
  const body = await browserPost(req, env);
  const a = await requireSession(req, env);
  if (a.user.role !== "admin") throw new HttpError(403, "only an admin makes invites");
  requireStepUp(a);
  const kind = body.kind === "recover" ? "recover" : "enroll";
  const d = dir(env);
  let name: string;
  let userId: string | null = null;
  if (kind === "recover") {
    const u = await d.user(str(body.userId, "userId", 40));
    if (!u) throw new HttpError(404, "no such account");
    userId = u.id;
    name = u.name;
  } else {
    name = cleanName(body.name, "name");
  }
  const role: Role = body.role === "admin" ? "admin" : "user";
  const token = randomToken();
  await d.createInvite(await sha256("invite:" + token), { name, role, kind, userId, createdBy: a.user.id, expires: Date.now() + INVITE_LIFE });
  // The token rides in the fragment: it never reaches a server log.
  return json({ url: `${env.RELAY_ORIGIN}/enroll#${token}`, expires: Date.now() + INVITE_LIFE });
}

// revokeAllOnMachines tells each of the user's machines, best effort, to
// drop Connect sessions of one device (or all). The grant check catches
// anything this misses within 60 seconds.
export async function revokeAllOnMachines(env: Env, userId: string, deviceId?: string) {
  for (const m of await dir(env).machines(userId)) {
    if (m.status === "active") await notifyMachine(env, m, deviceId ? { op: "revoke", dev: deviceId } : { op: "revoke" }).catch(() => {});
  }
}
