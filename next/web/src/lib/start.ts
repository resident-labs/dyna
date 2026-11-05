import type { BoxStartStatus, StartStep } from "./contract.gen.ts";
import { onHost } from "./hosts.ts";

const steps: Record<StartStep, string> = {
  instructions: "syncing instructions", binary: "updating Dyna on host", build_cache: "starting compiler cache",
  shared: "mounting shared folder", image: "preparing image", slice: "setting resource limits", forward: "forwarding socket",
  stack: "creating compose stack", container_create: "creating container", sidecars: "starting sidecars", container_start: "starting container",
};

// The session's wait on this step, not the age of the host's shared installation.
export function startLabel(start: BoxStartStatus | undefined, host: string | undefined, now: number): string {
  const place = onHost("Starting", start?.host ?? host);
  if (!start) return place;
  const elapsed = now - Date.parse(start.startedAt);
  return elapsed < 60_000 ? place : `${place} · ${steps[start.step]} (${Math.floor(elapsed / 60_000)} min)`;
}
