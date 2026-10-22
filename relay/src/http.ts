// Shared HTTP rules for the relay. Every API route is a POST with a JSON
// body, the relay's own Origin and the X-RL: 1 header. The relay answers
// no CORS, so no other site can send such a request with the session
// cookie. GET routes only render pages and have no side effects.

import type { Directory, Session, User } from "./directory";
import { sha256 } from "./crypto";

export interface Env {
  DIRECTORY: DurableObjectNamespace<Directory>;
  RELAY_ORIGIN: string;
  RP_ID: string;
  RP_NAME: string;
  MACHINE_ZONE: string;
  // MACHINE_URL is where the relay sends mint and revoke requests, with
  // {id} for the machine ID. It is the machine's tunnel host.
  MACHINE_URL: string;
  CF_API_BASE: string;
  CF_ACCOUNT_ID: string;
  CF_ZONE_ID: string;
  CF_API_TOKEN: string;
  RELAY_SIGNING_KEYS: string;
  BOOTSTRAP_INVITE_HASH?: string;
  BOOTSTRAP_NAME?: string;
}

export class HttpError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly code?: string,
  ) {
    super(message);
  }
}

export function dir(env: Env): DurableObjectStub<Directory> {
  return env.DIRECTORY.get(env.DIRECTORY.idFromName("directory"));
}

const securityHeaders: Record<string, string> = {
  "X-Content-Type-Options": "nosniff",
  "Referrer-Policy": "no-referrer",
  "X-Frame-Options": "DENY",
  "Cache-Control": "no-store",
};

export function json(v: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(v), {
    status,
    headers: { ...securityHeaders, "Content-Type": "application/json; charset=utf-8", ...headers },
  });
}

export function errorResponse(e: unknown): Response {
  if (e instanceof HttpError) return json({ error: e.message, code: e.code }, e.status);
  // Unknown errors keep their text out of the answer and out of logs that
  // could carry request data; the message names only the failing step.
  console.error("relay error:", e instanceof Error ? e.message : String(e));
  return json({ error: "the relay failed; try again" }, 500);
}

export function withSecurity(res: Response, extra: Record<string, string> = {}): Response {
  const h = new Headers(res.headers);
  for (const [k, v] of Object.entries({ ...securityHeaders, ...extra })) if (!h.has(k)) h.set(k, v);
  return new Response(res.body, { status: res.status, headers: h });
}

export function cookieName(env: Env): string {
  // __Host- needs Secure, so a plain http dev origin uses a bare name.
  return env.RELAY_ORIGIN.startsWith("https://") ? "__Host-rl_session" : "rl_session";
}

export function sessionCookie(env: Env, value: string, maxAgeSec: number): string {
  const secure = env.RELAY_ORIGIN.startsWith("https://") ? "; Secure" : "";
  return `${cookieName(env)}=${value}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${maxAgeSec}${secure}`;
}

export function readCookie(req: Request, name: string): string {
  const raw = req.headers.get("Cookie") ?? "";
  for (const part of raw.split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === name) return v.join("=");
  }
  return "";
}

// browserPost checks a browser API call: POST, the relay's own Origin,
// X-RL: 1 and a JSON body. It returns the parsed body.
export async function browserPost<T = Record<string, unknown>>(req: Request, env: Env): Promise<T> {
  if (req.method !== "POST") throw new HttpError(405, "use POST");
  if (req.headers.get("Origin") !== env.RELAY_ORIGIN) throw new HttpError(403, "the request did not come from the relay page");
  if (req.headers.get("X-RL") !== "1") throw new HttpError(403, "missing X-RL header");
  if (!(req.headers.get("Content-Type") ?? "").startsWith("application/json")) throw new HttpError(415, "the body must be JSON");
  const text = await req.text();
  if (text.length > 64 * 1024) throw new HttpError(413, "the body is too large");
  try {
    const v = JSON.parse(text || "{}");
    if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error();
    return v as T;
  } catch {
    throw new HttpError(400, "the body must be a JSON object");
  }
}

export interface Auth {
  session: Session;
  user: User;
  tokenHash: string;
}

export async function currentSession(req: Request, env: Env): Promise<Auth | null> {
  const tok = readCookie(req, cookieName(env));
  if (!tok) return null;
  const h = await sha256(tok);
  const d = dir(env);
  const session = await d.session(h);
  if (!session) return null;
  const user = await d.user(session.userId);
  if (!user) return null;
  return { session, user, tokenHash: h };
}

export async function requireSession(req: Request, env: Env): Promise<Auth> {
  const a = await currentSession(req, env);
  if (!a) throw new HttpError(401, "sign in first", "sign-in");
  return a;
}

export function requireStepUp(a: Auth) {
  if (Date.now() - a.session.steppedUpAt > 5 * 60_000) throw new HttpError(401, "confirm with your passkey first", "step-up");
}

export function clientIp(req: Request): string {
  return req.headers.get("CF-Connecting-IP") ?? "local";
}

export async function limit(env: Env, key: string, max: number, windowMs: number) {
  if (!(await dir(env).hit(key, max, windowMs))) throw new HttpError(429, "too many tries; wait and try again");
}

export function str(v: unknown, name: string, max = 200): string {
  if (typeof v !== "string" || v.length === 0 || v.length > max) throw new HttpError(400, `${name} is missing or too long`);
  return v;
}

// cleanName keeps a display name printable and short.
export function cleanName(v: unknown, name: string): string {
  const s = str(v, name, 200)
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .trim()
    .slice(0, 60);
  if (!s) throw new HttpError(400, `${name} is empty`);
  return s;
}
