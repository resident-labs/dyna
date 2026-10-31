import type { ShellJob } from "./shell.ts";

export const failed = (job: ShellJob) => !job.running && job.exitCode !== 0;
const rank = (job: ShellJob) => job.running ? 0 : failed(job) ? 1 : 2;

// The dock's job list (Jobs fix 2): per session, running jobs, then failed, then passed, each newest first. Each
// session's jobs come in the order the view saw them start or end, so newest first is that order reversed. Sessions
// follow their most urgent job. Passed jobs that ended over an hour ago go to older, which the list folds; a failed
// job stays in view until the owner deals with it. A job's end is by the box's clock, so a small skew moves only the fold.
export function jobGroups(sessions: { id: string; jobs: Iterable<ShellJob> }[], now: number): { session: string; jobs: ShellJob[]; older: ShellJob[] }[] {
  const old = (job: ShellJob) => rank(job) === 2 && job.ended !== undefined && now - Date.parse(job.ended) > 3_600_000;
  return sessions.map(({ id, jobs }) => {
    const sorted = [...jobs].reverse().sort((a, b) => rank(a) - rank(b));
    return { session: id, jobs: sorted.filter(job => !old(job)), older: sorted.filter(old) };
  }).filter(group => group.jobs.length + group.older.length).sort((a, b) => rank(a.jobs[0] ?? a.older[0]) - rank(b.jobs[0] ?? b.older[0]));
}

// How long a finished job ran (Jobs fix 5), such as "38 s", "2 min" or "1 h 5 min". Both ends are the box's clock, so
// the span is exact; a running job has no end to compare with this page's clock, so it has none.
export function jobTime(job: ShellJob): string | undefined {
  if (job.ended === undefined) return undefined;
  const seconds = Math.round((Date.parse(job.ended) - Date.parse(job.started)) / 1000);
  const minutes = Math.round(seconds / 60);
  return seconds < 60 ? `${seconds} s` : minutes < 60 ? `${minutes} min` : `${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} min` : ""}`;
}

// Where a failed job's output opens (Jobs fix 3): the first line that reports a failure, such as go test's
// "--- FAIL" or a compiler's "error:", as a range of the text; none when no line does.
export function failingLine(text: string): { start: number; end: number } | undefined {
  const match = /^.*\b(FAIL|FAILED|ERROR|Error|error|panic)\b.*$/m.exec(text);
  return match ? { start: match.index, end: match.index + match[0].length } : undefined;
}
