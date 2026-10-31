export function reloadDecision(draft: boolean, attachments: boolean, terminal: boolean): "reload" | "notice" {
  return draft || attachments || terminal ? "notice" : "reload";
}
