// Browser storage is untrusted and may contain an older or incomplete draft.
export function recoverInstructionDraft(raw: string, storedHash: string, current: { text: string; hash: string }) {
  let value: unknown;
  try { value = JSON.parse(raw); } catch { return; }
  if (!value || typeof value !== "object" || !("text" in value) || typeof value.text !== "string" ||
      !("base" in value) || typeof value.base !== "string") return;
  const stale = storedHash !== current.hash;
  return { draft: value.text, base: stale ? value.base : current.text, hash: stale ? storedHash : current.hash, stale };
}
