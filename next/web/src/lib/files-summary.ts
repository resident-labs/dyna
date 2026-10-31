import type { DraftFile } from "./attach.svelte.ts";

export function filesSummary(items: Pick<DraftFile, "state">[]): string {
  const failed = items.filter(item => item.state === "failed").length;
  const saving = items.filter(item => item.state === "saving").length;
  return [`${items.length} ${items.length === 1 ? "file" : "files"}`,
    failed ? `${failed} not saved` : "", saving ? `${saving} saving` : ""].filter(Boolean).join(" · ");
}
