// Cloudflare API calls for managed endpoints: one tunnel per machine
// (remotely managed config) and proxied CNAMEs for its app and card host
// names. The runtime token needs only Account Cloudflare Tunnel: Edit and
// Zone DNS: Edit on the machine zone. Every record the relay makes carries the comment "rl-connect
// <machine id>", and the relay changes or deletes only records with its
// own comment.

export interface CfEnv {
  CF_API_BASE: string;
  CF_ACCOUNT_ID: string;
  CF_ZONE_ID: string;
  CF_API_TOKEN: string;
}

export class CfError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function cf<T>(env: CfEnv, method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(env.CF_API_BASE + path, {
    method,
    headers: { Authorization: `Bearer ${env.CF_API_TOKEN}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: "manual",
  });
  const j = (await res.json().catch(() => null)) as { success?: boolean; result?: T; errors?: { message: string }[] } | null;
  if (!res.ok || !j?.success) {
    // Error text from Cloudflare names the resource, never the token.
    throw new CfError(res.status, `cloudflare ${method} ${path.split("?")[0]}: ${res.status} ${j?.errors?.map((e) => e.message).join("; ") ?? ""}`);
  }
  return j.result as T;
}

export const tunnelName = (machineId: string) => `rl-connect-${machineId}`;
export const recordComment = (machineId: string) => `rl-connect ${machineId}`;

export async function findTunnel(env: CfEnv, machineId: string): Promise<string | null> {
  const list = await cf<{ id: string }[]>(env, "GET", `/accounts/${env.CF_ACCOUNT_ID}/cfd_tunnel?is_deleted=false&name=${tunnelName(machineId)}`);
  return list[0]?.id ?? null;
}

export async function createTunnel(env: CfEnv, machineId: string): Promise<string> {
  const t = await cf<{ id: string }>(env, "POST", `/accounts/${env.CF_ACCOUNT_ID}/cfd_tunnel`, {
    name: tunnelName(machineId),
    config_src: "cloudflare",
  });
  return t.id;
}

// configureTunnel points the app host at the app port and the card host
// at the card port, both on loopback, and answers 404 for anything else.
// The relay never accepts a host or URL from the machine here: only two
// port numbers.
export async function configureTunnel(env: CfEnv, tunnelId: string, appHost: string, cardHost: string, appPort: number, cardPort: number) {
  await cf(env, "PUT", `/accounts/${env.CF_ACCOUNT_ID}/cfd_tunnel/${tunnelId}/configurations`, {
    config: {
      ingress: [
        { hostname: appHost, service: `http://127.0.0.1:${appPort}` },
        { hostname: cardHost, service: `http://127.0.0.1:${cardPort}` },
        { service: "http_status:404" },
      ],
    },
  });
}

export async function tunnelToken(env: CfEnv, tunnelId: string): Promise<string> {
  return cf<string>(env, "GET", `/accounts/${env.CF_ACCOUNT_ID}/cfd_tunnel/${tunnelId}/token`);
}

export async function deleteTunnel(env: CfEnv, tunnelId: string) {
  try {
    await cf(env, "DELETE", `/accounts/${env.CF_ACCOUNT_ID}/cfd_tunnel/${tunnelId}/connections`);
  } catch (e) {
    if (!(e instanceof CfError) || e.status !== 404) throw e;
  }
  try {
    await cf(env, "DELETE", `/accounts/${env.CF_ACCOUNT_ID}/cfd_tunnel/${tunnelId}`);
  } catch (e) {
    if (!(e instanceof CfError) || e.status !== 404) throw e;
  }
}

interface DnsRecord {
  id: string;
  type: string;
  name: string;
  content: string;
  comment?: string;
}

async function records(env: CfEnv, name: string): Promise<DnsRecord[]> {
  return cf<DnsRecord[]>(env, "GET", `/zones/${env.CF_ZONE_ID}/dns_records?name=${encodeURIComponent(name)}&per_page=50`);
}

// putRecord makes or updates the one record of this type at name. It
// refuses when a record there is not the relay's own for this machine.
export async function putRecord(env: CfEnv, machineId: string, type: string, name: string, content: string, proxied: boolean) {
  const comment = recordComment(machineId);
  const existing = await records(env, name);
  const foreign = existing.find((r) => r.comment !== comment);
  if (foreign) throw new CfError(409, `the DNS name ${name} already has a record the relay did not make`);
  const same = existing.filter((r) => r.type === type);
  const body = { type, name, content, proxied, ttl: proxied ? 1 : 60, comment };
  if (same.length) {
    await cf(env, "PUT", `/zones/${env.CF_ZONE_ID}/dns_records/${same[0].id}`, body);
    for (const r of same.slice(1)) await cf(env, "DELETE", `/zones/${env.CF_ZONE_ID}/dns_records/${r.id}`);
  } else {
    await cf(env, "POST", `/zones/${env.CF_ZONE_ID}/dns_records`, body);
  }
}

export async function deleteRecords(env: CfEnv, machineId: string, name: string) {
  const comment = recordComment(machineId);
  for (const r of await records(env, name)) {
    if (r.comment === comment) await cf(env, "DELETE", `/zones/${env.CF_ZONE_ID}/dns_records/${r.id}`);
  }
}
