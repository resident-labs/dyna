// list marks a list page (ui/settings), whose open item is at /settings/<id>/<item>.
export const settingsPanes: readonly { id: string; label: string; icon: "palette" | "devices" | "plug" | "tool" | "shield" | "plus"; admin: boolean; list?: boolean }[] = [
  { id: "appearance", label: "Appearance", icon: "palette", admin: false },
  { id: "devices", label: "Sharing & access", icon: "devices", admin: true },
  { id: "accounts", label: "Accounts", icon: "plug", admin: true },
  { id: "profiles", label: "Profiles", icon: "tool", admin: true },
  { id: "approvals", label: "Approvals", icon: "shield", admin: true, list: true },
  { id: "addons", label: "Skills & tools", icon: "plus", admin: false },
];
export const roomRoute = /^\/r\/([^/]+)(?:\/s\/([^/]+)|\/instructions|\/new)?\/?$/;
export const settingsRoute = /^\/settings(\/|$)/;
