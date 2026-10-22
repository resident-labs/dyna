// A software passkey for tests: ES256, attestation "none", user
// verification set. It makes the JSON a browser would send.

const enc = new TextEncoder();

function b64u(b: ArrayBuffer | Uint8Array): string {
  const u = b instanceof Uint8Array ? b : new Uint8Array(b);
  let s = "";
  for (const c of u) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function cat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

// Minimal CBOR: unsigned and negative ints, byte and text strings, maps.
function head(major: number, n: number): Uint8Array {
  if (n < 24) return new Uint8Array([(major << 5) | n]);
  if (n < 256) return new Uint8Array([(major << 5) | 24, n]);
  return new Uint8Array([(major << 5) | 25, n >> 8, n & 255]);
}
type Cbor = number | string | Uint8Array | Map<Cbor, Cbor>;
function cbor(v: Cbor): Uint8Array {
  if (typeof v === "number") return v >= 0 ? head(0, v) : head(1, -1 - v);
  if (typeof v === "string") {
    const b = enc.encode(v);
    return cat(head(3, b.length), b);
  }
  if (v instanceof Uint8Array) return cat(head(2, v.length), v);
  const parts: Uint8Array[] = [head(5, v.size)];
  for (const [k, x] of v) parts.push(cbor(k), cbor(x));
  return cat(...parts);
}

function der(raw: Uint8Array): Uint8Array {
  const int = (b: Uint8Array) => {
    let i = 0;
    while (i < b.length - 1 && b[i] === 0) i++;
    let x = b.slice(i);
    if (x[0] & 0x80) x = cat(new Uint8Array([0]), x);
    return cat(new Uint8Array([2, x.length]), x);
  };
  const body = cat(int(raw.slice(0, 32)), int(raw.slice(32)));
  return cat(new Uint8Array([0x30, body.length]), body);
}

async function sha(b: Uint8Array): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", b));
}

function unb64u(s: string): Uint8Array {
  const p = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  return Uint8Array.from(atob(p), (c) => c.charCodeAt(0));
}

export class Passkey {
  id = crypto.getRandomValues(new Uint8Array(16));
  counter = 0;
  userHandle: Uint8Array | null = null;
  private constructor(
    private keys: CryptoKeyPair,
    readonly rpId: string,
    readonly origin: string,
  ) {}

  static async make(rpId: string, origin: string) {
    const kp = (await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"])) as CryptoKeyPair;
    return new Passkey(kp, rpId, origin);
  }

  async create(options: { challenge: string; user: { id: string } }) {
    this.userHandle = unb64u(options.user.id);
    const jwk = (await crypto.subtle.exportKey("jwk", this.keys.publicKey)) as JsonWebKey;
    const cose = cbor(
      new Map<Cbor, Cbor>([
        [1, 2],
        [3, -7],
        [-1, 1],
        [-2, unb64u(jwk.x!)],
        [-3, unb64u(jwk.y!)],
      ]),
    );
    const authData = cat(
      await sha(enc.encode(this.rpId)),
      new Uint8Array([0x45, 0, 0, 0, 0]),
      new Uint8Array(16),
      new Uint8Array([0, this.id.length]),
      this.id,
      cose,
    );
    const clientData = enc.encode(JSON.stringify({ type: "webauthn.create", challenge: options.challenge, origin: this.origin, crossOrigin: false }));
    const att = cbor(
      new Map<Cbor, Cbor>([
        ["fmt", "none"],
        ["attStmt", new Map()],
        ["authData", authData],
      ]),
    );
    return {
      id: b64u(this.id),
      rawId: b64u(this.id),
      type: "public-key",
      clientExtensionResults: {},
      response: { clientDataJSON: b64u(clientData), attestationObject: b64u(att), transports: ["internal"] },
    };
  }

  async get(options: { challenge: string }) {
    this.counter++;
    const c = this.counter;
    const authData = cat(await sha(enc.encode(this.rpId)), new Uint8Array([0x05, c >> 24, (c >> 16) & 255, (c >> 8) & 255, c & 255]));
    const clientData = enc.encode(JSON.stringify({ type: "webauthn.get", challenge: options.challenge, origin: this.origin, crossOrigin: false }));
    const sig = new Uint8Array(
      await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, this.keys.privateKey, cat(authData, await sha(clientData))),
    );
    return {
      id: b64u(this.id),
      rawId: b64u(this.id),
      type: "public-key",
      clientExtensionResults: {},
      response: {
        clientDataJSON: b64u(clientData),
        authenticatorData: b64u(authData),
        signature: b64u(der(sig)),
        userHandle: this.userHandle ? b64u(this.userHandle) : undefined,
      },
    };
  }
}
