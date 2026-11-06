export type PanelTab = "sessions" | "about" | "files";

// A conversation remembers its own choice; About is only available in an open DM.
export function panelTab(tabs: ReadonlyMap<string, PanelTab>, conversation: string, dm: boolean): PanelTab {
  const chosen = tabs.get(conversation);
  if (chosen === "files") return "files";
  return dm ? chosen ?? "about" : "sessions";
}
