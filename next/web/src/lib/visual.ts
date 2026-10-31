import type { VisualCard, VisualCheck, VisualCheckName } from "./contract.gen.ts";
import { APIError } from "./client.ts";
import type { PostEvent } from "./window.svelte.ts";

export function visualReplyFailure(_error: unknown): { kind: "failed" } | { kind: "superseded"; visual: import("./contract.gen.ts").VisualSuperseded } {
  return _error instanceof APIError && _error.detail.code === "visual_superseded" && _error.detail.visual
    ? { kind: "superseded", visual: _error.detail.visual } : { kind: "failed" };
}

export type VisualVersion = { event: PostEvent; visual: VisualCard };

// The iframe is untrusted. Parse its message once before it reaches the viewer.
export function cardMessage(value: unknown): { type: "open"; url: string } | { type: "submit"; text: string } | undefined {
  if (typeof value !== "object" || value === null || !("type" in value)) return;
  if (value.type === "dyna:open" && "url" in value && typeof value.url === "string" && value.url.length <= 2048 && URL.canParse(value.url)) {
    const url = new URL(value.url);
    if (url.protocol === "http:" || url.protocol === "https:") return { type: "open", url: url.href };
  }
  if (value.type === "dyna:submit" && "text" in value && typeof value.text === "string" && value.text.length <= 16384 &&
      (!("choice" in value) || value.choice === undefined || typeof value.choice === "string" && value.choice.length <= 16384)) {
    return { type: "submit", text: value.text };
  }
}

export function checkLabel(check: VisualCheck): string {
  const labels: Record<VisualCheckName, string> = {
    renders: "Renders", console: "No console errors", widths: "Widths", contrast: "Contrast", network: "Network",
  };
  return check.name === "console" && check.status !== "pass" ? "Console errors" : labels[check.name];
}

// Check details come from the renderer. Show typed facts instead of its raw output.
export function worstCheck(checks: VisualCheck[] | null): string {
  const check = checks?.find(item => item.status === "fail") ?? checks?.find(item => item.status === "warn");
  if (!check) return "";
  const facts: Record<VisualCheckName, string> = {
    renders: "Did not render in its check", console: "Console errors", widths: "Overflows at checked widths",
    contrast: "Contrast needs review", network: "Network requests blocked",
  };
  if (check.name === "widths") {
    const widths = check.detail?.match(/^Overflow at widths: (\d+(?:, \d+)*)$/)?.[1];
    if (widths) return `Overflows at ${widths} px`;
  }
  return facts[check.name];
}
