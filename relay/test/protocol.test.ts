import { describe, expect, it } from "vitest";
import vectors from "../../testdata/connect/protocol.json";
import { fingerprint, importEd25519Public, parseJws, parseSigningKeys, sha256, signJws, unb64url, verifyEd25519, verifyJws } from "../src/crypto";
import { mintMessage, type MachineClaims, type MachineInfo, type RelayClaims } from "../src/protocol";

describe("shared Connect protocol vectors", () => {
  it("matches fixed fingerprints", async () => {
    for (const v of vectors.fingerprints) expect(await fingerprint(unb64url(v.publicKey))).toBe(v.fingerprint);
  });

  it("verifies the Go machine request and its body hash", async () => {
    const jws = parseJws(vectors.machineRequest.jws);
    expect(jws.header.typ).toBe("dyna-machine+jws");
    expect(await verifyJws(jws, await importEd25519Public(unb64url(vectors.machinePublic)))).toBe(true);
    const claims: MachineClaims = vectors.machineRequest.claims;
    expect(jws.payload).toEqual(claims);
    expect(await sha256(vectors.machineRequest.body)).toBe(claims.bh);
  });

  it("signs the relay request checked by Go", async () => {
    const [key] = await parseSigningKeys(JSON.stringify([{ kty: "OKP", crv: "Ed25519", kid: "compat-1", d: vectors.relaySeed, x: vectors.relayPublic }]));
    const claims: RelayClaims = { ...vectors.relayRequest.claims, op: "mint" };
    expect(await signJws(key, claims, "dyna-connect+jws")).toBe(vectors.relayRequest.jws);
    const info: MachineInfo = vectors.machineInfo;
    expect(claims.aud).toBe(info.id);
    expect(claims.sub).toBe(info.owner.id);
    expect(claims.epoch).toBe(info.epoch);
    expect(claims.origin).toBe(info.app);
  });

  it("verifies the machine mint response", async () => {
    const message = await mintMessage(vectors.mint.nonce, vectors.mint.code);
    expect(message).toBe(vectors.mint.message);
    const key = await importEd25519Public(unb64url(vectors.machinePublic));
    expect(await verifyEd25519(key, unb64url(vectors.mint.sig), message)).toBe(true);
    expect(await verifyEd25519(key, unb64url(vectors.mint.sig), await mintMessage(vectors.mint.nonce, "wrong-code"))).toBe(false);
  });
});
