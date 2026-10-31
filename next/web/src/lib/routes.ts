import type { IconName } from "./ui/Icon.svelte";

export const settingsPanes: { id: string; label: string; icon: IconName; admin?: boolean }[] = [
  { id: "appearance", label: "Appearance", icon: "palette" },
  { id: "devices", label: "Sharing & access", icon: "devices", admin: true },
  { id: "accounts", label: "Accounts", icon: "plug", admin: true },
  { id: "profiles", label: "Profiles", icon: "tool", admin: true },
];
export const roomRoute = /^\/r\/([^/]+)(?:\/s\/([^/]+))?\/?$/;
export const settingsRoute = /^\/settings(\/|$)/;
