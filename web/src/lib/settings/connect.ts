// Helpers for Resident Labs Connect in the browser: the device key, proofs,
// the pinned machine key, route ranking and the hello check. Nothing here
// touches the app's state, so each piece is easy to read on its own. The
// flows that use them live in link.ts.
import { isRecord } from '../api/guards.ts'

const enc = new TextEncoder()

export function b64uEncode(bytes: ArrayBuffer | ArrayLike<number>): string {
  let s = ''
  for (const b of bytes instanceof ArrayBuffer ? new Uint8Array(bytes) : Array.from(bytes)) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function b64uDecode(text: string): Uint8Array<ArrayBuffer> {
  const s = text.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(s + '='.repeat((4 - (s.length % 4)) % 4))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0))
}

export function randomB64u(n = 16): string {
  return b64uEncode(crypto.getRandomValues(new Uint8Array(n)))
}

// IndexedDB: one database, two stores. "keys" holds this origin's device key
// pair; "pins" holds the machine key each machine showed us at sign-in.
let dbp: Promise<IDBDatabase> | null = null
function db(): Promise<IDBDatabase> {
  dbp ??= new Promise<IDBDatabase>((ok, fail) => {
    const req = indexedDB.open('dyna-connect', 1)
    req.onupgradeneeded = () => {
      req.result.createObjectStore('keys')
      req.result.createObjectStore('pins')
    }
    req.onsuccess = () => ok(req.result)
    req.onerror = () => {
      dbp = null
      fail(req.error ?? new Error('could not open the key database'))
    }
  })
  return dbp
}

async function idb<T>(store: string, mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const d = await db()
  return new Promise<T>((ok, fail) => {
    const req = fn(d.transaction(store, mode).objectStore(store))
    req.onsuccess = () => ok(req.result)
    req.onerror = () => fail(req.error ?? new Error('the key database refused the request'))
  })
}

// deviceKey returns this origin's key pair. The private key cannot be
// exported, so a stolen cookie or page script cannot copy it out.
export async function deviceKey(): Promise<CryptoKeyPair> {
  const have: unknown = await idb('keys', 'readonly', (s) => s.get('device'))
  if (isKeyPair(have)) return have
  const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign'])
  await idb('keys', 'readwrite', (s) => s.put(pair, 'device'))
  return pair
}

// A pinned machine key: the key the machine showed at sign-in.
export interface Pin {
  machine: string
  key: string
}
const isKeyPair = (v: unknown): v is CryptoKeyPair => typeof v === 'object' && v !== null && 'publicKey' in v && 'privateKey' in v
export const getPin = (machine: string): Promise<Pin | undefined> => idb<Pin | undefined>('pins', 'readonly', (s) => s.get(machine))
export const putPin = (machine: string, key: string): Promise<IDBValidKey> => idb('pins', 'readwrite', (s) => s.put({ machine, key }, machine))

// publicJwk is the public half as the four members RFC 7638 uses.
// The four members of an EC public key that RFC 7638 uses.
export interface PublicJwk {
  kty?: string
  crv?: string
  x?: string
  y?: string
}
export async function publicJwk(pair: CryptoKeyPair): Promise<PublicJwk> {
  const { kty, crv, x, y } = await crypto.subtle.exportKey('jwk', pair.publicKey)
  return { kty, crv, x, y }
}

// thumbprint is the RFC 7638 thumbprint (jkt) of an EC public JWK.
export async function thumbprint(jwk: PublicJwk): Promise<string> {
  const json = `{"crv":"${jwk.crv}","kty":"${jwk.kty}","x":"${jwk.x}","y":"${jwk.y}"}`
  return b64uEncode(await crypto.subtle.digest('SHA-256', enc.encode(json)))
}

// makeProof builds a DPoP-style compact JWS for one request. The signature
// from WebCrypto is already the raw r||s form that ES256 wants.
export async function makeProof(pair: CryptoKeyPair, method: string, url: string): Promise<string> {
  const part = (o: object) => b64uEncode(enc.encode(JSON.stringify(o)))
  const head = part({ typ: 'dpop+jwt', alg: 'ES256', jwk: await publicJwk(pair) })
  const body = part({ htm: method, htu: url, iat: Math.floor(Date.now() / 1000), jti: randomB64u(16) })
  const sig = await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, enc.encode(`${head}.${body}`))
  return `${head}.${body}.${b64uEncode(sig)}`
}

// The nearer a route, the lower its rank.
const RANK: Record<string, number> = { loopback: 0, tailscale: 1, custom: 2, tunnel: 3 }
export const rank = (route: { kind: string }): number => RANK[route.kind] ?? 3

export const ROUTE_LABEL: Record<string, string> = { loopback: 'this computer', tailscale: 'Tailscale', custom: 'a custom route', tunnel: 'tunnel' }

// A route may carry a session only over https, or over http on this computer.
export function allowedRoute(app: string): boolean {
  try {
    const u = new URL(app)
    if (u.protocol === 'https:') return true
    return u.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(u.hostname)
  } catch {
    return false
  }
}

// What a route answers to the hello check.
interface Hello {
  machine: string
  nonce: string
  origin: string
  sig: string
}
const isHello = (v: unknown): v is Hello => isRecord(v) && typeof v.machine === 'string' && typeof v.nonce === 'string' && typeof v.origin === 'string' && typeof v.sig === 'string'

// verifyHello asks a route who it is and checks the answer against the
// machine key we pinned. It is true only for a correct signature over our
// own fresh nonce. A browser without Ed25519 gets false: no switch.
export async function verifyHello(app: string, machine: string, keyB64u: string): Promise<boolean> {
  const nonce = randomB64u(16)
  let key: CryptoKey
  try {
    key = await crypto.subtle.importKey('raw', b64uDecode(keyB64u), { name: 'Ed25519' }, false, ['verify'])
  } catch {
    return false
  }
  const ctl = new AbortController()
  const timer = setTimeout(() => ctl.abort(), 3000)
  try {
    const r = await fetch(`${app}/connect/v1/hello?nonce=${nonce}`, { mode: 'cors', credentials: 'omit', signal: ctl.signal })
    if (!r.ok) return false
    const h: unknown = await r.json()
    if (!isHello(h)) return false
    if (h.machine !== machine || h.nonce !== nonce || h.origin !== app) return false
    const msg = enc.encode(`dyna-connect-hello.v1\n${h.machine}\n${h.origin}\n${h.nonce}`)
    return await crypto.subtle.verify({ name: 'Ed25519' }, key, b64uDecode(h.sig), msg)
  } catch {
    return false
  } finally {
    clearTimeout(timer)
  }
}
