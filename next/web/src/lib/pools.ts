import type { HostSummary, Pool, PoolSetRequest } from "./contract.gen.ts";

// Settings > Pools (spec 5.13): the node pools (5.17) from pool.list, and each one's nodes from host.list. The engine sends
// a pool's idle time in milliseconds, and none for a pool with one node per session, which has no idle time.

// Pools by name with digits as numbers, gpu-2 before gpu-10.
export const poolOrder = (pools: readonly Pool[]): Pool[] => [...pools].sort((a, b) => a.name.localeCompare(b.name, "en", { numeric: true }));

// Minutes as the field and the row show them, up to two decimals, as the CLI may set 90 s.
export const idleMinutes = (ms: number) => Math.round(ms / 600) / 100;

// "3 of 4 nodes · idle 30 min", or "· one per session".
export const poolLine = (p: Pool) =>
  `${p.nodes.toLocaleString("en")} of ${p.max.toLocaleString("en")} ${p.max === 1 ? "node" : "nodes"} · ${p.perSession ? "one per session" : `idle ${idleMinutes(p.idle ?? 0).toLocaleString("en")} min`}`;

// A pool's nodes that host.list holds, by name with digits as numbers, and how many more are still being created, which
// pool.list counts and host.list does not list yet.
export function poolNodes(p: Pool, hosts: readonly HostSummary[]): { hosts: HostSummary[]; creating: number } {
  const own = hosts.filter(h => h.pool === p.name).sort((a, b) => a.name.localeCompare(b.name, "en", { numeric: true }));
  return { hosts: own, creating: Math.max(0, p.nodes - own.length) };
}

export const poolSummary = (pools: readonly Pool[]) =>
  ({ pools: pools.length, nodes: pools.reduce((sum, p) => sum + p.nodes, 0), failing: pools.filter(p => p.problem).length });

// The pool.set for a field's text, or undefined when it holds no number (At most takes a whole one) or the pool already
// has that value as the field shows it, so the field is put back or left alone; an idle time the field rounds, such as
// 100 ms, stays unless the minutes change. A time above zero is at least 1 ms, as 0 would make 30 minutes. The engine
// checks the ranges.
export function fieldChange(p: Pool, field: "max" | "idle", text: string): PoolSetRequest | undefined {
  const n = text.trim() === "" ? NaN : Number(text);
  if (!Number.isFinite(n)) return undefined;
  if (field === "max") return Number.isInteger(n) && n !== p.max ? { name: p.name, max: n } : undefined;
  return n !== idleMinutes(p.idle ?? 0) ? { name: p.name, idle: n > 0 ? Math.max(1, Math.round(n * 60_000)) : Math.round(n * 60_000) } : undefined;
}

// The changes waiting while one saves: a change to a pool already waiting joins its request, the later value winning
// for each field, so each pool waits as one pool.set; another pool's waits behind it.
export function enqueue(queue: readonly PoolSetRequest[], request: PoolSetRequest): PoolSetRequest[] {
  const at = queue.findIndex(each => each.name === request.name);
  return at < 0 ? [...queue, request] : queue.map((each, i) => i === at ? { ...each, ...request } : each);
}

export type PoolField = "max" | "idle" | "perSession";
// The field a refusal of a request is about, the first it sets in the order the engine checks them.
export const refusedField = (request: PoolSetRequest): PoolField =>
  request.max !== undefined ? "max" : request.idle !== undefined ? "idle" : "perSession";
// The fields a request sets.
export const requestFields = (request: PoolSetRequest): PoolField[] =>
  (["max", "idle", "perSession"] as const).filter(field => request[field] !== undefined);
