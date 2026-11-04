// list marks a list page (ui/settings), whose open item is at /settings/<id>/<item>. sep sets a pane apart from the
// ones above it in the nav with a line, as the machines Dyna runs on are.
export const settingsPanes: readonly { id: string; label: string; icon: "palette" | "devices" | "plug" | "tool" | "shield" | "plus" | "monitor"; admin: boolean; list?: boolean; sep?: boolean }[] = [
  { id: "appearance", label: "Appearance", icon: "palette", admin: false },
  { id: "devices", label: "Sharing & access", icon: "devices", admin: true, list: true },
  { id: "accounts", label: "Accounts", icon: "plug", admin: true, list: true },
  { id: "profiles", label: "Profiles", icon: "tool", admin: true, list: true },
  { id: "approvals", label: "Approvals", icon: "shield", admin: true, list: true },
  { id: "addons", label: "Skills & tools", icon: "plus", admin: false, list: true },
  { id: "hosts", label: "Hosts", icon: "monitor", admin: true, list: true, sep: true },
];
// The counts beside the Settings panes in the nav (spec 5.13), only from lists the app already holds: Approvals lists
// the rooms not being deleted. rooms is undefined until they load, and then no pane has a count.
export function paneCounts(rooms: readonly { deleting?: boolean }[] | undefined): Partial<Record<string, number>> {
  return rooms ? { approvals: rooms.filter(room => !room.deleting).length } : {};
}
export const roomRoute = /^\/r\/([^/]+)(?:\/s\/([^/]+)|\/instructions|\/new)?\/?$/;
export const settingsRoute = /^\/settings(\/|$)/;

// The one part of path after prefix, decoded: a Settings list page's open item. undefined when path does not start
// with prefix, the part is empty or holds a slash, or its escapes are malformed, so a pasted /settings/approvals/% reads
// as no item instead of throwing in the page. decodeURIComponent has no form that reports a malformed escape without
// throwing, hence the catch.
export function pathItem(path: string, prefix: string): string | undefined {
  if (!path.startsWith(prefix)) return undefined;
  const part = path.slice(prefix.length);
  if (!part || part.includes("/")) return undefined;
  try { return decodeURIComponent(part); } catch { return undefined; }
}
