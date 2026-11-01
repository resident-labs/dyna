import type { VisualCard, VisualSuperseded } from "./contract.gen.ts";
import { APIError } from "./client.ts";
import type { PostEvent } from "./window.svelte.ts";

export function visualReplyFailure(error: unknown): { kind: "failed" } | { kind: "superseded"; visual: VisualSuperseded } {
  return error instanceof APIError && error.detail.code === "visual_superseded" && error.detail.visual
    ? { kind: "superseded", visual: error.detail.visual } : { kind: "failed" };
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
