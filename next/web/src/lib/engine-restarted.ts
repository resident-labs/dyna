import type { EngineRestarted } from "./contract.gen.ts";

export type RestartPart = { text: string; href?: string };

// The notice has the same words as room.RenderDelivery. Only the web links builds.
export function engineRestartParts(event: EngineRestarted): RestartPart[] {
  const build = (sha: string): RestartPart => ({
    text: sha.slice(0, 12),
    .../^[0-9a-f]{7,40}$/i.test(sha) && { href: `https://github.com/resident-labs/dyna/commit/${sha}` },
  });
  const parts: RestartPart[] = [{ text: "Dyna restarted on build " }, build(event.build)];
  if (!event.clean) return [...parts, { text: " after an unclean stop (was " }, build(event.previousBuild), { text: ")." }];
  if (event.build !== event.previousBuild) parts.push({ text: " (was " }, build(event.previousBuild), { text: ")" });
  return [...parts, { text: `; it was down ${Math.trunc(event.downMillis / 1000)} s.` }];
}

export const engineRestartText = (event: EngineRestarted): string => engineRestartParts(event).map(part => part.text).join("");
