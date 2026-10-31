import type { ShellJobOutput } from "./contract.gen.ts";
import { decodeShellOutput } from "./event.ts";

// The engine sends each job's last 64 KiB on a reset, so the view keeps no more than that.
export const KEEP = 64 * 1024;
// keepFinished in box/shell.
export const KEEP_FINISHED = 20;

export type ShellJob = Omit<ShellJobOutput, "reset">;

// Applies one streamed message. A reset, such as the first message after a reconnect, replaces the
// job's text; any other message appends to it. Kept text starts at the first whole line in the last KEEP characters.
export function applyOutput(jobs: Map<string, ShellJob>, { reset, ...output }: ShellJobOutput): void {
  let text = reset ? output.text : (jobs.get(output.job)?.text ?? "") + output.text;
  if (text.length > KEEP) {
    const cut = text.length - KEEP, line = text.indexOf("\n", cut - 1);
    text = text.slice(line < 0 ? cut : line + 1);
  }
  // A job that ends moves behind the jobs that ended before it, so the map's order is when the view saw each start or end.
  if (jobs.get(output.job)?.running && !output.running) jobs.delete(output.job);
  jobs.set(output.job, { ...output, text });
  // The box keeps its running jobs and its last KEEP_FINISHED finished ones, so the view drops the same older jobs.
  for (const old of [...jobs.values()].filter(job => !job.running).slice(0, -KEEP_FINISHED)) jobs.delete(old.job);
}

export interface ShellConnection {
  onopen: ((event: globalThis.Event) => void) | null;
  onmessage: ((message: MessageEvent<string>) => void) | null;
  onerror: ((event: globalThis.Event) => void) | null;
  readonly readyState: number;
  close(): void;
}

// Watches a session's jobs into the map until the returned stop runs. Every open, EventSource's own reconnects
// included, starts a new watch generation that first resets each job the box still keeps. So the map starts
// empty then, and jobs the box pruned meanwhile leave the view. refused gets true once the engine refuses for good.
export function watchShell(session: string, jobs: Map<string, ShellJob>, refused: (closed: boolean) => void,
  factory: (url: string) => ShellConnection = url => new EventSource(url)): () => void {
  const source = factory(`/v1/shell/watch?${new URLSearchParams({ session })}`);
  source.onopen = () => jobs.clear();
  source.onmessage = message => { const output = decodeShellOutput(JSON.parse(message.data)); if (output) applyOutput(jobs, output); };
  source.onerror = () => refused(source.readyState === 2); // EventSource.CLOSED: it stops retrying
  return () => source.close();
}
