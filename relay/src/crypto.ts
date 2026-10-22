// Small crypto helpers on WebCrypto: base64url, hashes, random IDs and
// compact JWS with Ed25519 (alg EdDSA). The relay signs with its own keys
// and checks machine signatures with the machine's stored public key.

const enc = new TextEncoder();
const dec = new TextDecoder();

export function b64url(bytes: ArrayBuffer | Uint8Array): string {
  const b = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let s = "";
  for (const c of b) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function unb64url(s: string): Uint8Array<ArrayBuffer> {
  if (!/^[A-Za-z0-9_-]*$/.test(s)) throw new Error("bad base64url");
  const p = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  const bin = atob(p);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export async function sha256(data: string | Uint8Array): Promise<string> {
  const d = typeof data === "string" ? enc.encode(data) : data;
  return b64url(await crypto.subtle.digest("SHA-256", d));
}

export function randomToken(bytes = 32): string {
  return b64url(crypto.getRandomValues(new Uint8Array(bytes)));
}

// Lowercase base32 without look-alike letters. Machine IDs use it, so a
// host name like m-a1b2c3d4e5 is safe in DNS and easy to read.
const idAlphabet = "abcdefghjkmnpqrstuvwxyz23456789";

export function randomId(n = 10): string {
  const b = crypto.getRandomValues(new Uint8Array(n));
  let s = "";
  for (const c of b) s += idAlphabet[c % idAlphabet.length];
  return s;
}

// User codes for the link flow: 8 characters, shown as XXXX-XXXX.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";

export function userCode(): string {
  const b = crypto.getRandomValues(new Uint8Array(8));
  let s = "";
  for (const c of b) s += codeAlphabet[c % 32];
  return s.slice(0, 4) + "-" + s.slice(4);
}

export function normalizeUserCode(s: string): string {
  const c = s.toUpperCase().replace(/[^A-Z0-9]/g, "");
  return c.length === 8 ? c.slice(0, 4) + "-" + c.slice(4) : "";
}

export function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

// --- Ed25519 keys ---

export interface SigningKey {
  kid: string;
  key: CryptoKey; // private
  publicJwk: JsonWebKey & { kid: string; alg: string; use: string };
}

// parseSigningKeys reads RELAY_SIGNING_KEYS: a JSON array of Ed25519
// private JWKs, each with a kid. The first key signs; every key is listed
// in the key set, so machines accept tokens from the next key before the
// switch.
export async function parseSigningKeys(raw: string): Promise<SigningKey[]> {
  const list = JSON.parse(raw) as (JsonWebKey & { kid: string })[];
  if (!Array.isArray(list) || list.length === 0) throw new Error("RELAY_SIGNING_KEYS is empty");
  return Promise.all(
    list.map(async (j) => {
      if (j.kty !== "OKP" || j.crv !== "Ed25519" || !j.d || !j.x || !j.kid) throw new Error("RELAY_SIGNING_KEYS: want Ed25519 private JWKs with kid");
      const key = await crypto.subtle.importKey("jwk", { kty: "OKP", crv: "Ed25519", d: j.d, x: j.x }, { name: "Ed25519" }, false, ["sign"]);
      return { kid: j.kid, key, publicJwk: { kty: "OKP", crv: "Ed25519", x: j.x, kid: j.kid, alg: "EdDSA", use: "sig" } };
    }),
  );
}

export async function newSigningKeyJwk(kid: string): Promise<JsonWebKey & { kid: string }> {
  const kp = (await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"])) as CryptoKeyPair;
  const jwk = (await crypto.subtle.exportKey("jwk", kp.privateKey)) as JsonWebKey;
  return { ...jwk, kid };
}

export async function importEd25519Public(raw: Uint8Array): Promise<CryptoKey> {
  if (raw.length !== 32) throw new Error("an Ed25519 public key has 32 bytes");
  return crypto.subtle.importKey("raw", raw, { name: "Ed25519" }, false, ["verify"]);
}

// --- compact JWS ---

export async function signJws(key: SigningKey, payload: object, typ: string): Promise<string> {
  const head = b64url(enc.encode(JSON.stringify({ alg: "EdDSA", kid: key.kid, typ })));
  const body = b64url(enc.encode(JSON.stringify(payload)));
  const sig = await crypto.subtle.sign({ name: "Ed25519" }, key.key, enc.encode(head + "." + body));
  return head + "." + body + "." + b64url(sig);
}

export interface Jws {
  header: { alg: string; kid?: string; typ?: string };
  payload: Record<string, unknown>;
  signingInput: Uint8Array;
  signature: Uint8Array;
}

export function parseJws(token: string): Jws {
  const parts = token.split(".");
  if (parts.length !== 3) throw new Error("not a compact JWS");
  const header = JSON.parse(dec.decode(unb64url(parts[0])));
  const payload = JSON.parse(dec.decode(unb64url(parts[1])));
  if (typeof header !== "object" || header === null || typeof payload !== "object" || payload === null) throw new Error("bad JWS");
  return { header, payload, signingInput: enc.encode(parts[0] + "." + parts[1]), signature: unb64url(parts[2]) };
}

export async function verifyJws(jws: Jws, pub: CryptoKey): Promise<boolean> {
  if (jws.header.alg !== "EdDSA") return false;
  return crypto.subtle.verify({ name: "Ed25519" }, pub, jws.signature, jws.signingInput);
}

export async function verifyEd25519(pub: CryptoKey, sig: Uint8Array, data: string): Promise<boolean> {
  return crypto.subtle.verify({ name: "Ed25519" }, pub, sig, enc.encode(data));
}

// fingerprint is how a person compares a machine key on two screens:
// the first 16 bytes of SHA-256 of the raw key, as 4 groups of base32.
export async function fingerprint(raw: Uint8Array): Promise<string> {
  const h = new Uint8Array(await crypto.subtle.digest("SHA-256", raw));
  let s = "";
  for (let i = 0; i < 16; i++) s += codeAlphabet[h[i] % 32];
  return s.match(/.{4}/g)!.join("-");
}
