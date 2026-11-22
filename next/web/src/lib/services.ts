import type { Service, ServiceReason, ServiceState, SubscriptionInfo } from "./contract.gen.ts";
import { fromNow } from "./feed.ts";
import { hookName } from "./subscriptions.ts";

const reasons: Partial<Record<ServiceReason, string>> = {
  cpu: "Waiting for CPU",
  memory: "Waiting for memory",
  disk: "Waiting for disk",
  "room-host": "No ready room host",
  "room-limit": "At room service limit",
  service_host: "Host not ready for services",
  port_taken: "Port in use",
  port_in_use: "Port in use",
  port_privileged: "Privileged port",
  secret_missing: "Missing secret",
  image_build: "Image build failed",
  image_pull: "Image pull failed",
  unit_failed: "Unit failed",
  start_failed: "Start failed",
  health_failed: "Health check failed",
  crash_loop: "Crash loop",
  rolled_back: "Rolled back",
  host_unreachable: "Host unreachable",
  host_removed: "Host removed",
};

const states: Record<ServiceState, string> = {
  pending: "Pending",
  deploying: "Deploying",
  running: "Running",
  crashing: "Crashing",
  stopped: "Stopped",
  failed: "Failed",
  unreachable: "Unreachable",
  removing: "Removing",
};

const isBadState = (state: ServiceState): boolean => state === "failed" || state === "crashing" || state === "unreachable";

const scriptLabel = (script: string): string => {
  const base = (script.split("/").at(-1) ?? script).replace(/\.(js|mjs|cjs|ts|sh|py)$/, "").replace(/[-_.]+/g, " ").trim();
  return base || script;
};

// The room's services as one line in About ("Services 2"), with how they stand.
// Failures surface immediately; otherwise show deploying/pending or "all running" when every active service is running.
export function serviceSummary(list: readonly Service[]): { count: number; health: string; failing: boolean } {
  const active = list.filter(service => service.want !== "remove" && service.state !== "removing");
  const failed = active.filter(service => isBadState(service.state)).length;
  if (failed) return { count: active.length, health: `${failed} failing`, failing: true };
  const deploying = active.filter(service => service.state === "deploying").length;
  const pending = active.filter(service => service.state === "pending").length;
  const running = active.filter(service => service.state === "running").length;
  if (deploying) return { count: active.length, health: `${deploying} deploying`, failing: false };
  if (pending) return { count: active.length, health: `${pending} pending`, failing: false };
  const health = active.length > 0 && running === active.length ? "all running" : running > 0 ? `${running} running` : "";
  return { count: active.length, health, failing: false };
}

function matchesService(where: readonly string[] | null | undefined, serviceName: string): boolean {
  const filters = (where ?? []).filter(clause => clause.startsWith("service=")).map(clause => clause.slice("service=".length));
  return filters.length === 0 || filters.includes(serviceName);
}

// Enabled subscriptions that route service.message events from this feed service.
export function serviceRoutes(service: Service, subscriptions: readonly SubscriptionInfo[], name: (session: string) => string): string[] {
  const routes: string[] = [];
  for (const info of subscriptions) {
    if (info.disabled) continue;
    const sub = info.subscription;
    if (!sub.on?.includes("service.message")) continue;
    if (!matchesService(sub.where, service.spec.name)) continue;
    if (sub.wake) routes.push(`→ wakes ${name(sub.wake)}`);
    else if (sub.workflow) routes.push(`→ runs ${scriptLabel(sub.workflow.script)}`);
    else if (sub.run) routes.push(`→ runs ${hookName(sub.run.command).name}`);
  }
  return routes;
}

export function serviceError(service: Service): string {
  const code = service.reason || service.cause;
  const label = code ? (reasons[code] ?? code.replace(/[_-]+/g, " ")) : "";
  const detail = service.detail?.trim() ?? "";
  if (label && detail && detail.toLowerCase() !== label.toLowerCase()) return `${label}: ${detail}`;
  return detail || label;
}

export interface ServiceRow {
  id: string;
  name: string;
  host: string;
  state: string;
  failing: boolean;
  stopped: boolean;
  error: string;
  feed: boolean;
  lastMessage: string;
  lastMessageAt: string | null;
  messagesIn: number;
  messagesOut: number;
  routes: string[];
}

const stateRank: Record<ServiceState, number> = { failed: 0, crashing: 1, unreachable: 2, deploying: 3, pending: 4, running: 5, stopped: 6, removing: 7 };

// Rows for About's Services modal: failures first, then active, then stopped, with feed counters and routing.
export function serviceRows(
  list: readonly Service[],
  subscriptions: readonly SubscriptionInfo[],
  name: (session: string) => string,
  now = Date.now(),
): ServiceRow[] {
  return list
    .filter(service => service.want !== "remove" && service.state !== "removing")
    .slice()
    .sort((a, b) => stateRank[a.state] - stateRank[b.state] || a.spec.name.localeCompare(b.spec.name))
    .map(service => {
      const isFeed = Boolean(service.spec.feed || service.feed);
      const lastAt = service.feed?.lastMessageAt ?? null;
      return {
        id: service.id,
        name: service.spec.name,
        host: service.host ?? "",
        state: states[service.state],
        failing: isBadState(service.state),
        stopped: service.state === "stopped",
        error: serviceError(service),
        feed: isFeed,
        lastMessage: lastAt ? fromNow(lastAt, Math.max(now, Date.parse(lastAt))) : "No messages yet",
        lastMessageAt: lastAt,
        messagesIn: service.feed?.messagesIn ?? 0,
        messagesOut: service.feed?.messagesOut ?? 0,
        routes: isFeed ? serviceRoutes(service, subscriptions, name) : [],
      };
    });
}
