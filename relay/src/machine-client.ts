// Managed origins and signed requests from the relay to a machine.
import { parseSigningKeys, randomToken, signJws } from "./crypto";
import type { Machine } from "./directory";
import type { Env } from "./http";
import type { RelayPayload } from "./protocol";

export function managedApp(env: Env, machineId: string): string {
  return `https://m-${machineId}.${env.MACHINE_ZONE}`;
}

export function managedCards(env: Env, machineId: string): string {
  return `https://mc-${machineId}.${env.MACHINE_ZONE}`;
}

export async function relaySign(env: Env, m: Machine, payload: RelayPayload): Promise<{ jws: string; nonce: string }> {
  const keys = await parseSigningKeys(env.RELAY_SIGNING_KEYS);
  const now = Math.floor(Date.now() / 1000);
  const nonce = randomToken(16);
  const jws = await signJws(keys[0], { ...payload, iss: env.RELAY_ORIGIN, aud: m.id, nonce, iat: now, exp: now + 60 }, "dyna-connect+jws");
  return { jws, nonce };
}

export function machineUrl(env: Env, m: Machine, path: string): string {
  // Built from the relay's own record, never from a request parameter.
  return env.MACHINE_URL.replace("{id}", m.id) + path;
}

export async function notifyMachine(env: Env, m: Machine, payload: RelayPayload) {
  const { jws } = await relaySign(env, m, { ...payload, epoch: payload.epoch ?? m.epoch });
  await fetch(machineUrl(env, m, "/connect/v1/revoke"), {
    method: "POST",
    headers: { "Content-Type": "application/jose" },
    body: jws,
    redirect: "manual",
    signal: AbortSignal.timeout(5000),
  });
}

