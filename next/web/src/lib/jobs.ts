import type { ShellJobEnd } from "./contract.gen.ts";
import type { ShellJob } from "./shell.ts";

// LEGACY(v1-transfer): the exit-code fallback in endOf and the exitCode reads below, for events without the end field. Remove after the transfer.
// How a finished job ended. Events from before the end field carry only the exit code, where -1 means a signal ended it.
type Ended = { exitCode: number; end?: ShellJobEnd | null };
const endOf = ({ exitCode, end }: Ended): ShellJobEnd => end ?? (exitCode === -1 ? { kind: "signal" } : { kind: "exit", code: exitCode });
// A nonzero exit or a signal is a failure; a stop that its session asked for is not.
export function failed(job: Ended & { running?: boolean }): boolean {
  if (job.running) return false;
  const end = endOf(job);
  return end.kind === "signal" || end.kind === "exit" && (end.code ?? job.exitCode) !== 0;
}
// The end cause in words, such as "Exit 2", "Ended by a signal (SIGTERM)", "Stopped by root" or "Passed".
export function endCause(job: Ended, session: string): string {
  const end = endOf(job);
  if (end.kind === "signal") return end.signal ? `Ended by a signal (${end.signal})` : "Ended by a signal";
  if (end.kind === "stopped") return `Stopped by ${session}`;
  const code = end.code ?? job.exitCode;
  return code ? `Exit ${code}` : "Passed";
}
// A job's title: the description its session gave, or else its command's first line.
export const jobTitle = (job: ShellJob) => job.description || job.command.split("\n", 1)[0];
const rank = (job: ShellJob) => job.running ? 0 : failed(job) ? 1 : 2;

// The jobs list: per session, running jobs, then failed, then the rest, each newest first. Each session's jobs come in
// the order the view saw them start or end, so newest first is that order reversed. Sessions follow their most urgent
// job. Past the newest RECENT finished jobs that did not fail, the rest go to older, behind "Show older"; a failed job
// stays in view until the owner deals with it.
const RECENT = 3;
export function jobGroups(sessions: { id: string; jobs: Iterable<ShellJob> }[]): { session: string; jobs: ShellJob[]; older: ShellJob[] }[] {
  return sessions.map(({ id, jobs }) => {
    const sorted = [...jobs].reverse().sort((a, b) => rank(a) - rank(b));
    const settled = sorted.filter(job => rank(job) === 2);
    return { session: id, jobs: sorted.filter(job => rank(job) < 2).concat(settled.slice(0, RECENT)), older: settled.slice(RECENT) };
  }).filter(group => group.jobs.length).sort((a, b) => rank(a.jobs[0]) - rank(b.jobs[0]));
}

// How long a finished job ran, such as "38 s", "2 min" or "1 h 5 min". Both ends are the box's clock, so the span is
// exact; a running job has no end to compare with this page's clock, so it has none.
export function jobTime(job: ShellJob): string | undefined {
  if (job.ended === undefined) return undefined;
  const seconds = Math.round((Date.parse(job.ended) - Date.parse(job.started)) / 1000);
  const minutes = Math.round(seconds / 60);
  return seconds < 60 ? `${seconds} s` : minutes < 60 ? `${minutes} min` : `${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} min` : ""}`;
}

// Where a failed job's output opens: the first line that reports a failure, such as go test's "--- FAIL" or a
// compiler's "error:", as a range of the text; none when no line does.
export function failingLine(text: string): { start: number; end: number } | undefined {
  const match = /^.*\b(FAIL|FAILED|ERROR|Error|error|panic)\b.*$/m.exec(text);
  return match ? { start: match.index, end: match.index + match[0].length } : undefined;
}
