export const settingsPanes = [
  { id: "appearance", label: "Appearance", icon: "palette", admin: false },
  { id: "devices", label: "Sharing & access", icon: "devices", admin: true },
  { id: "accounts", label: "Accounts", icon: "plug", admin: true },
  { id: "profiles", label: "Profiles", icon: "tool", admin: true },
] as const;
export const roomRoute = /^\/r\/([^/]+)(?:\/s\/([^/]+)|\/instructions)?\/?$/;
export const settingsRoute = /^\/settings(\/|$)/;
