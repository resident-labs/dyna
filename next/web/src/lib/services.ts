import type { DiscordRoute, Service, ServiceReason, ServiceState, SubscriptionInfo } from "./contract.gen.ts";
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

// The room's services and built-in feeds as one line in About ("Services 2"), with how they stand.
// Failures surface immediately; otherwise show deploying/pending or "all running" when every active entry is healthy.
export function serviceSummary(
  list: readonly Service[],
  discord: readonly DiscordRoute[] = [],
): { count: number; health: string; failing: boolean } {
  const active = list.filter(service => service.want !== "remove" && service.state !== "removing");
  const total = active.length + discord.length;
  const failedServices = active.filter(service => isBadState(service.state)).length;
  const failedDiscord = discord.filter(route => Boolean(route.gatewayError?.trim())).length;
  const failed = failedServices + failedDiscord;
  if (failed) return { count: total, health: `${failed} failing`, failing: true };
  const deploying = active.filter(service => service.state === "deploying").length;
  const pending = active.filter(service => service.state === "pending").length;
  const running = active.filter(service => service.state === "running").length + (discord.length - failedDiscord);
  if (deploying) return { count: total, health: `${deploying} deploying`, failing: false };
  if (pending) return { count: total, health: `${pending} pending`, failing: false };
  const health = total > 0 && running === total ? "all running" : running > 0 ? `${running} running` : "";
  return { count: total, health, failing: false };
}

function matchesService(where: readonly string[] | null | undefined, serviceName: string): boolean {
  for (const clause of where ?? []) {
    if (clause.startsWith("service=")) {
      if (clause.slice("service=".length) !== serviceName) return false;
    } else if (clause.startsWith("service!=")) {
      if (clause.slice("service!=".length) === serviceName) return false;
    }
  }
  return true;
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
    else if (sub.workflow) routes.push("→ runs workflow");
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
  builtIn: boolean;
  detail: string;
  lastMessage: string;
  lastMessageAt: string | null;
  messagesIn: number;
  messagesOut: number;
  routes: string[];
}

const stateRank: Record<ServiceState, number> = { failed: 0, crashing: 1, unreachable: 2, deploying: 3, pending: 4, running: 5, stopped: 6, removing: 7 };

function discordRow(route: DiscordRoute, name: (session: string) => string): { rank: number; sortKey: string; row: ServiceRow } {
  const error = route.gatewayError?.trim() ?? "";
  const failing = Boolean(error);
  const channel = route.channel?.trim() ?? "";
  const channelName = route.channelName?.trim() ?? "";
  const guildName = route.guildName?.trim() ?? "";
  let host = "";
  let detail = "";
  if (channel) {
    host = channelName ? `#${channelName}` : `channel ${channel}`;
    if (guildName && channelName) detail = `${guildName} · channel ${channel}`;
    else if (guildName) detail = guildName;
    else if (channelName) detail = `server ${route.guild} · channel ${channel}`;
    else detail = `server ${route.guild}`;
  } else {
    host = guildName || `server ${route.guild}`;
    detail = guildName ? `All channels · server ${route.guild}` : "All channels";
  }
  return {
    rank: failing ? 0 : 5,
    sortKey: `discord:${route.guild}:${channel}`,
    row: {
      id: `discord:${route.guild}:${channel || "*"}`,
      name: "Discord",
      host,
      state: failing ? "Failed" : "Active",
      failing,
      stopped: false,
      error,
      feed: true,
      builtIn: true,
      detail,
      lastMessage: "",
      lastMessageAt: null,
      messagesIn: 0,
      messagesOut: 0,
      routes: route.session ? [`→ wakes ${name(route.session)}`] : [],
    },
  };
}

// Rows for About's Services modal: failures first, then active (including built-in Discord feeds), then stopped.
export function serviceRows(
  list: readonly Service[],
  subscriptions: readonly SubscriptionInfo[],
  name: (session: string) => string,
  now = Date.now(),
  discord: readonly DiscordRoute[] = [],
): ServiceRow[] {
  const entries: { rank: number; sortKey: string; row: ServiceRow }[] = list
    .filter(service => service.want !== "remove" && service.state !== "removing")
    .map(service => {
      const isFeed = Boolean(service.spec.feed || service.feed);
      const lastAt = service.feed?.lastMessageAt ?? null;
      return {
        rank: stateRank[service.state],
        sortKey: service.spec.name,
        row: {
          id: service.id,
          name: service.spec.name,
          host: service.host ?? "",
          state: states[service.state],
          failing: isBadState(service.state),
          stopped: service.state === "stopped",
          error: serviceError(service),
          feed: isFeed,
          builtIn: false,
          detail: "",
          lastMessage: lastAt ? fromNow(lastAt, Math.max(now, Date.parse(lastAt))) : "No messages yet",
          lastMessageAt: lastAt,
          messagesIn: service.feed?.messagesIn ?? 0,
          messagesOut: service.feed?.messagesOut ?? 0,
          routes: isFeed ? serviceRoutes(service, subscriptions, name) : [],
        },
      };
    });
  for (const route of discord) entries.push(discordRow(route, name));
  entries.sort((a, b) => a.rank - b.rank || a.sortKey.localeCompare(b.sortKey));
  return entries.map(entry => entry.row);
}
