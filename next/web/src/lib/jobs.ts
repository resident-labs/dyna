import type { ShellJob } from "./shell.ts";

export const failed = (job: ShellJob) => !job.running && job.exitCode !== 0;
const rank = (job: ShellJob) => job.running ? 0 : failed(job) ? 1 : 2;

// The dock's job list (Jobs fix 2): per session, running jobs, then failed, then passed, each newest first. Each
// session's jobs come in the order the view saw them start or end, so newest first is that order reversed. Sessions
// follow their most urgent job.
export function jobGroups(sessions: { id: string; jobs: Iterable<ShellJob> }[]): { session: string; jobs: ShellJob[] }[] {
  return sessions.map(({ id, jobs }) => ({ session: id, jobs: [...jobs].reverse().sort((a, b) => rank(a) - rank(b)) }))
    .filter(group => group.jobs.length).sort((a, b) => rank(a.jobs[0]) - rank(b.jobs[0]));
}

// Where a failed job's output opens (Jobs fix 3): the first line that reports a failure, such as go test's
// "--- FAIL" or a compiler's "error:", as a range of the text; none when no line does.
export function failingLine(text: string): { start: number; end: number } | undefined {
  const match = /^.*\b(FAIL|FAILED|ERROR|Error|error|panic)\b.*$/m.exec(text);
  return match ? { start: match.index, end: match.index + match[0].length } : undefined;
}
