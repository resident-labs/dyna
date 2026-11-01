import type { BoxState, Event, ShellJobOutput } from "./contract.gen.ts";

type Check = (value: unknown) => boolean;
const string: Check = value => typeof value === "string";
const number: Check = value => typeof value === "number" && Number.isFinite(value);
const boolean: Check = value => typeof value === "boolean";
const oneOf = (...values: string[]): Check => value => typeof value === "string" && values.includes(value);
const optional = (check: Check): Check => value => value === undefined || check(value);
const nullable = (check: Check): Check => value => value === null || check(value);
const array = (check: Check): Check => value => Array.isArray(value) && value.every(check);
const object = (fields: Record<string, Check>): Check => value => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  // The object check above makes string-keyed property access safe.
  const record = value as Record<string, unknown>;
  return Object.entries(fields).every(([key, check]) => check(record[key]));
};

const computerState = oneOf("running", "stopped");
const principal = object({ kind: oneOf("owner", "session", "engine"), session: optional(string) });
const conversation = object({ kind: oneOf("feed", "dm"), session: optional(string) });
const reply = object({ kind: oneOf("final", "post", "none"), target: optional(principal), home: optional(conversation), replyTo: optional(string) });
const usage = object({ model: string, inputTokens: number, outputTokens: number, cacheReadTokens: number, cacheWriteTokens: number });
const card = object({
  title: string, body: optional(string), target: object({ kind: oneOf("repo", "room", "host", "session", "account"), name: string }),
  changes: optional(nullable(array(object({ field: string, old: string, new: string })))),
  command: optional(nullable(array(string))),
});
const state = oneOf("pending", "approved", "denied", "expired", "withdrawn", "done", "failed");
const cause = optional(oneOf("create_failed", "start_failed", "no_hello", "engine_restarted", "remove_failed", "archive_failed", "grant_inactive", "host_lost", "host_busy", "github_failed", "stale", "interrupted", "internal", "setup_failed", "model_failed", "output_limit", "harness_failed", "set_model_failed", "not_started", "nested_device_missing", "kvm_device_missing"));
// How a shell job ended (the end field is new, so it may be missing).
const jobEnd = optional(nullable(object({ kind: oneOf("exit", "signal", "stopped"), code: optional(number), signal: optional(string) })));
const boxState = oneOf("pending", "creating", "starting", "ready", "stopped", "closing", "deleted", "failed", "lost");
const visual = object({ id: string, version: number, title: string, height: number, allowNet: boolean, banner: string, thumbnail: string,
  shots: nullable(array(object({ width: number, file: string }))), checks: nullable(array(object({
    name: oneOf("renders", "console", "widths", "contrast", "network"), status: oneOf("pass", "warn", "fail", "skip"), detail: optional(string),
  }))) });
const prChangeKind = oneOf("ci_failed", "ci_passed", "changes_requested", "approved", "conflict", "merged", "closed", "comment");
const prSummary = object({ repo: string, number: number, url: string, title: string, author: string, session: string, draft: boolean, state: oneOf("open", "merged", "closed"), head: string,
  review: oneOf("", "approved", "changes_requested", "review_required"), ci: oneOf("", "none", "pending", "success", "failure"), merge: oneOf("", "clean", "blocked", "behind", "conflict") });
const bodies: Record<string, Check> = {
  "connect.connector": object({ missing: boolean }),
  "account.failed": object({ accountId: string, reason: oneOf("sign_in_rejected", "refresh_failed") }),
  "account.quota_low": object({ accountId: string, window: string, remainingPercent: number, floor: number }),
  "post.created": object({ text: string, home: conversation, turn: optional(string), mentions: optional(nullable(array(principal))), replyTo: optional(string), clientId: optional(string),
    attachments: optional(nullable(array(object({ name: string, media: string, bytes: number, sha256: string, visual: optional(nullable(visual)) })))) }),
  "session.restarted": object({ session: string, imageUpdated: optional(boolean) }),
  "session.started": object({ session: string, name: string, parent: optional(string), profile: string, goal: optional(string), doneWhen: optional(string), brief: optional(string) }),
  "session.moved": object({ session: string, newParent: string }),
  "session.closed": object({ session: string, reason: optional(oneOf("dormant", "disk")), archive: optional(string), unheld: optional(nullable(array(string))) }),
  "computer.changed": object({ session: string, to: computerState }),
  "box.changed": object({ box: string, session: string, host: optional(string), from: optional(boxState), to: boxState, cause, reason: optional(string) }),
  "turn.started": object({ session: string, turn: string, inputs: nullable(array(string)) }),
  "turn.ended": object({ session: string, turn: string, outcome: oneOf("completed", "failed", "stopped"), final: optional(string),
    replies: optional(nullable(array(object({ reply, posted: optional(string) })))), usage, error: optional(string), cause, shellJobs: optional(number) }),
  "conversation.read": object({ room: string, conversation, seq: number }),
  "turn.activity": object({ session: string, turn: string, label: string }),
  "transcript.changed": object({ session: string, turn: string }),
  "op.requested": object({ id: string, kind: string, by: principal, class: oneOf("auto", "owner"), card, expires: string }),
  "op.decided": object({ id: string, state, decision: object({ by: principal, approved: boolean, reason: optional(string), at: string }) }),
  "op.ran": object({ id: string, state, result: object({ exitCode: number, cause, error: optional(string), output: optional(string), outputFile: optional(string) }) }),
  "question.asked": object({ id: string, session: string, text: string }),
  "question.answered": object({ id: string, session: string, question: string, answer: string }),
  "question.withdrawn": object({ id: string, session: string }),
  "shell.job.ended": object({ session: string, job: string, exitCode: number, tail: string, description: optional(string), end: jobEnd }),
  "box.pressure": object({ box: string, kind: oneOf("high", "max") }),
  "host.disk_low": object({ host: string, freeBytes: number, floorBytes: number }),
  "grant.expired": object({ grant: string }),
  "pr.opened": object({ repo: string, number: number, session: string, url: string }),
  "pr.changed": object({ session: string, changes: nullable(array(object({ pr: prSummary, kinds: optional(nullable(array(prChangeKind))), failedChecks: optional(nullable(array(string))),
    outside: boolean, comments: optional(nullable(array(object({ author: string, body: string, url: string, outside: boolean })))), moreComments: optional(number) }))) }),
  "room.paused": object({}),
  // Whole counts, zero for no limit. The engine allows memBytes past 2^53, about 8 PB; such a limit is skipped here.
  "room.limits_changed": object({ cpus: value => Number.isSafeInteger(value) && Number(value) >= 0, memBytes: value => Number.isSafeInteger(value) && Number(value) >= 0 }),
  "room.resumed": object({}),
  "room.deleting": object({}),
  "room.deleted": object({}),
  "session.profile_changed": object({ session: string, profile: string }),
  "session.failed": object({ to: string, failures: nullable(array(object({ session: string, name: string, parent: optional(string), box: optional(boxState), cause, reason: optional(string) }))) }),
  "goal.set": object({ goal: string, session: optional(string), text: string, doneWhen: optional(string) }),
  "goal.item_changed": object({ goal: string, item: number, text: string, state: oneOf("open", "done", "dropped") }),
  "goal.ended": object({ goal: string, session: optional(string), state: oneOf("active", "done", "dropped"), summary: string }),
  "checkin.due": object({ session: string, children: nullable(array(object({ session: string, name: string, parent: optional(string), state: oneOf("idle", "stopped", "blocked"), reason: optional(string),
    idle: number, silent: optional(boolean), goal: string, text: string, open: number }))) }),
  "image.built": object({ host: string, repo: string, name: string, tag: string, seconds: number }),
  "image.failed": object({ host: string, repo: string, name: string, tag: string, reason: string, logTail: string }),
};

// Every event from events.list and the stream passes here once. An event that does not
// match the contract is skipped with a console warning, so one bad event cannot block a room.
export function decodeEvent(value: unknown): Event | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return skip(value);
  // JSON objects have string keys; the checks below validate the generated event shape.
  const record = value as Record<string, unknown>;
  const body = typeof record.type === "string" && Object.hasOwn(bodies, record.type) ? bodies[record.type] : undefined;
  // Live-only events are never stored and have seq 0.
  if (!Number.isSafeInteger(record.seq) || Number(record.seq) < (record.type === "turn.activity" || record.type === "conversation.read" || record.type === "transcript.changed" ? 0 : 1) || !string(record.id) ||
      !string(record.at) || !optional(string)(record.room) || !principal(record.from) || !body?.(record.body)) {
    return skip(value);
  }
  // The validators above cover every member of the generated Event union.
  return value as Event;
}

const shellOutput = object({ job: string, command: string, running: boolean, exitCode: number, text: string, reset: optional(boolean),
  started: string, ended: optional(string), description: optional(string), end: jobEnd });
// Each message of /v1/shell/watch passes here once; one that does not match is skipped like an event.
export function decodeShellOutput(value: unknown): ShellJobOutput | undefined {
  // The check covers every field of the generated type.
  return shellOutput(value) ? value as ShellJobOutput : skip(value);
}

// Why a pending box waits: the reasons admission names (next/box/admission.go). A box reason, in box.changed or
// session.list, is free text for other states and for a create that will retry, so any other text gives no reason.
const pendingReasons = ["cpu", "memory", "disk", "room-limit", "no-ready-host", "room-host", "invalid-size"] as const;
export type PendingReason = typeof pendingReasons[number];
export const pendingReason = (to: BoxState, reason?: string): PendingReason | undefined =>
  to === "pending" ? pendingReasons.find(item => item === reason) : undefined;

function skip(value: unknown): undefined {
  console.warn("Skipped an event that does not match the contract", value);
  return undefined;
}
