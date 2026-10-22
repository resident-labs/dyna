// Prints a new Ed25519 private JWK with a kid, for RELAY_SIGNING_KEYS.
// Usage: node scripts/keygen.mjs [kid]
const kid = process.argv[2] || "k" + new Date().toISOString().slice(0, 10).replace(/-/g, "");
const kp = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
const jwk = await crypto.subtle.exportKey("jwk", kp.privateKey);
process.stdout.write(JSON.stringify({ kty: "OKP", crv: "Ed25519", d: jwk.d, x: jwk.x, kid }));
