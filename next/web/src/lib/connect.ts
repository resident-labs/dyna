// Browser Connect keys are origin-bound. The private key is never exportable.
const encoder = new TextEncoder();
export const encode = (bytes: ArrayBuffer | Uint8Array): string => {
  const input = bytes instanceof ArrayBuffer ? new Uint8Array(bytes) : bytes;
  return btoa(Array.from(input, byte => String.fromCharCode(byte)).join("")).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
};
export const decode = (value: string): Uint8Array<ArrayBuffer> => {
  const input = value.replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(input + "=".repeat((4 - input.length % 4) % 4)), char => char.charCodeAt(0));
};
export const random = () => encode(crypto.getRandomValues(new Uint8Array(16)));

export interface Pin { machine: string; key: string; relay: string; at: number; name?: string }
let database: Promise<IDBDatabase> | undefined;
function db(): Promise<IDBDatabase> {
  return database ??= new Promise((resolve, reject) => {
    const request = indexedDB.open("dyna-connect", 1);
    request.onupgradeneeded = () => { request.result.createObjectStore("keys"); request.result.createObjectStore("pins"); };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => { database = undefined; reject(request.error); };
  });
}
async function stored<T>(store: string, mode: IDBTransactionMode, action: (object: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const connection = await db();
  return new Promise((resolve, reject) => {
    const transaction = connection.transaction(store, mode);
    const request = action(transaction.objectStore(store));
    // A write succeeds only after the transaction commits.
    transaction.oncomplete = () => resolve(request.result);
    transaction.onerror = () => reject(transaction.error);
    transaction.onabort = () => reject(transaction.error);
  });
}
export const getPin = (machine: string): Promise<Pin | undefined> => stored("pins", "readonly", object => object.get(machine));
export const putPin = (pin: Pin) => stored("pins", "readwrite", object => object.put(pin, pin.machine));

let keyPromise: Promise<CryptoKeyPair> | undefined;
export function deviceKey(): Promise<CryptoKeyPair> {
  return keyPromise ??= (async () => {
    const existing: CryptoKeyPair | undefined = await stored("keys", "readonly", object => object.get("device"));
    if (existing) return existing;
    const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
    await stored("keys", "readwrite", object => object.put(pair, "device"));
    return pair;
  })().catch(error => { keyPromise = undefined; throw error; });
}
async function publicKey(pair: CryptoKeyPair) {
  const { crv, kty, x, y } = await crypto.subtle.exportKey("jwk", pair.publicKey);
  return { crv, kty, x, y };
}
export async function thumbprint(pair: CryptoKeyPair): Promise<string> {
  return encode(await crypto.subtle.digest("SHA-256", encoder.encode(JSON.stringify(await publicKey(pair)))));
}
export async function proof(pair: CryptoKeyPair, path: string): Promise<string> {
  const part = (value: object) => encode(encoder.encode(JSON.stringify(value)));
  const head = part({ typ: "dpop+jwt", alg: "ES256", jwk: await publicKey(pair) });
  const body = part({ htm: "POST", htu: location.origin + path, iat: Math.floor(Date.now() / 1000), jti: random() });
  const sig = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, pair.privateKey, encoder.encode(head + "." + body));
  return head + "." + body + "." + encode(sig);
}

// Only HTTPS can carry a session outside a loopback test or local browser.
export function allowedOrigin(value: string): boolean {
  try {
    const url = new URL(value);
    const loopback = url.hostname === "localhost" || url.hostname.endsWith(".localhost") || url.hostname === "127.0.0.1" || url.hostname === "[::1]";
    return url.origin === value && !url.username && !url.password && (url.protocol === "https:" || url.protocol === "http:" && loopback);
  } catch { return false; }
}
export interface Hello { machine: string; origin: string; nonce: string; sig: string; relay: string; key: string }
function isHello(value: unknown): value is Hello {
  if (typeof value !== "object" || value === null) return false;
  return "machine" in value && typeof value.machine === "string" && "origin" in value && typeof value.origin === "string" &&
    "nonce" in value && typeof value.nonce === "string" && "sig" in value && typeof value.sig === "string" &&
    "relay" in value && typeof value.relay === "string" && "key" in value && typeof value.key === "string";
}
export async function hello(origin: string): Promise<Hello | null> {
  if (!allowedOrigin(origin)) throw new Error("invalid origin");
  const nonce = random();
  const response = await fetch(origin + "/connect/v1/hello?nonce=" + nonce, { credentials: "omit", signal: AbortSignal.timeout(3000) });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error("hello failed");
  const data: unknown = await response.json();
  if (!isHello(data) || data.origin !== origin || data.nonce !== nonce) throw new Error("invalid hello");
  return data;
}
export async function verifyHello(hello: Hello, key: string): Promise<boolean> {
  try {
    const publicKey = await crypto.subtle.importKey("raw", decode(key), "Ed25519", false, ["verify"]);
    const message = encoder.encode("dyna-connect-hello.v1\n" + hello.machine + "\n" + hello.origin + "\n" + hello.nonce);
    return await crypto.subtle.verify("Ed25519", publicKey, decode(hello.sig), message);
  } catch { return false; }
}
// The same fingerprint appears on the hard stop and in Sharing & access.
export async function fingerprint(key: string): Promise<string> {
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", decode(key)));
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  return Array.from(hash.slice(0, 16), byte => alphabet[byte % 32]).join("").match(/.{4}/g)?.join("-") ?? "";
}
