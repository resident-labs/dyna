import type { ConnectStatusResponse, Device } from "./contract.gen.ts";
import { access, expiring, isLoopback, listed, seen } from "./devices.ts";
import type { Chip, Group } from "./settings-list.ts";

// What Settings > Sharing & access lists (spec 5.13): the ways into the engine from connect.status, then the paired
// browsers from device.list. key is the item's place in the path. current marks the way this page came through.
export type WayItem = { kind: "way"; key: string; name: string; address: string; local: boolean; current: boolean };
// Connect is listed even when its status did not load; then status is undefined.
export type ConnectItem = { kind: "connect"; key: "connect"; name: string; status?: ConnectStatusResponse; current: boolean };
// warning is the expiry warning a row shows, from 3 days left.
export type DeviceItem = { kind: "device"; key: string; device: Device; warning?: string };
export type SharingItem = WayItem | ConnectItem | DeviceItem;

export function wayName(address: string) {
  const host = new URL(address).hostname;
  return isLoopback(host) ? "Local address" : host.endsWith(".ts.net") ? "Tailscale HTTPS" : "Your HTTPS address";
}

// The configured routes in the order browsers try them, but Connect's tunnel, which Connect's own item stands for;
// then Connect; then the devices, this one first, then the newest.
export function sharingItems(status: ConnectStatusResponse | undefined, devices: readonly Device[], origin: string, now: number): SharingItem[] {
  const tunnel = status?.tunnel;
  const ways: SharingItem[] = (status?.routes ?? []).filter(route => route !== tunnel)
    .map(route => ({ kind: "way", key: `way:${new URL(route).host}`, name: wayName(route), address: route, local: isLoopback(new URL(route).hostname), current: route === origin }));
  ways.push({ kind: "connect", key: "connect", name: "Resident Labs Connect", status, current: tunnel === origin });
  return [...ways, ...listed(devices).map(device => ({ kind: "device" as const, key: device.id, device, warning: expiring(device.expiresAt, now) }))];
}

const ways: Group = { id: "ways", title: "Ways in" }, devices: Group = { id: "devices", title: "Devices" };
export const sharingGroup = (item: SharingItem) => item.kind === "device" ? devices : ways;

export const sharingChips: Chip<SharingItem>[] = [
  { id: "all", label: "All" },
  { id: "admin", label: "Admin", test: item => item.kind === "device" && !!item.device.scopes?.includes("admin") },
  { id: "expiring", label: "Expiring", test: item => item.kind === "device" && item.warning !== undefined },
];

export function sharingText(item: SharingItem) {
  if (item.kind === "way") return `${item.name} ${item.address}`;
  if (item.kind === "connect") return `${item.name} ${item.status?.tunnel ?? ""}`;
  return `${item.device.name} ${item.device.host ?? ""} ${access(item.device.scopes ?? [])}`;
}

// Connect's row line: a step under way, then its address once linked, then a code that waits for the owner.
export function connectLine(status: ConnectStatusResponse | undefined) {
  if (!status) return "Status did not load";
  if (status.attempt.state === "starting") return "Turning on…";
  if (status.attempt.state === "unlinking") return "Turning off…";
  if (status.linked) return status.tunnel ?? "On";
  return status.attempt.state === "pending" ? "Waiting for your approval" : "Off";
}

// On is a plain fact, so it has no tone; a stopped connector while Connect is on is a warning.
export function connectPill(status: ConnectStatusResponse | undefined): { label: string; tone?: "warning" } | undefined {
  if (!status?.linked) return undefined;
  return status.connectorMissing ? { label: "Connector stopped", tone: "warning" } : { label: "On" };
}

export function sharingLine(item: SharingItem, now: number) {
  if (item.kind === "way") return item.address;
  if (item.kind === "connect") return connectLine(item.status);
  const { device } = item;
  return `${seen(device, now)} · ${access(device.scopes ?? [])}${device.host ? ` · on ${device.host}` : ""}`;
}
