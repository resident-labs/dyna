import type { Event } from "./contract.gen.ts";

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
const boxState = oneOf("pending", "creating", "starting", "ready", "stopped", "closing", "deleted", "failed", "lost");
const bodies: Record<string, Check> = {
  "post.created": object({ text: string, home: conversation, mentions: optional(nullable(array(principal))), replyTo: optional(string),
    attachments: optional(nullable(array(object({ name: string, media: string, bytes: number, sha256: string })))) }),
  "session.started": object({ session: string, name: string, parent: optional(string), profile: string, goal: optional(string), brief: optional(string) }),
  "session.moved": object({ session: string, newParent: string }),
  "session.closed": object({ session: string, archive: optional(string), unheld: optional(nullable(array(string))) }),
  "computer.changed": object({ session: string, to: computerState }),
  "box.changed": object({ box: string, session: string, host: optional(string), from: optional(boxState), to: boxState, reason: optional(string) }),
  "turn.started": object({ session: string, turn: string, inputs: nullable(array(string)) }),
  "turn.ended": object({ session: string, turn: string, outcome: oneOf("completed", "failed", "stopped"), final: optional(string),
    replies: optional(nullable(array(object({ reply, posted: optional(string) })))), usage, error: optional(string) }),
  "op.requested": object({ id: string, kind: string, by: principal, class: oneOf("auto", "agent", "owner"), card, expires: string }),
  "op.decided": object({ id: string, state, decision: object({ by: principal, approved: boolean, reason: optional(string), at: string }) }),
  "op.ran": object({ id: string, state, result: object({ exitCode: number, error: optional(string), output: optional(string), outputFile: optional(string) }) }),
  "shell.job.ended": object({ session: string, job: string, exitCode: number, tail: string }),
  "box.pressure": object({ box: string, kind: oneOf("high", "max") }),
  "grant.expired": object({ grant: string }),
  "pr.opened": object({ repo: string, number: number, session: string, url: string }),
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
  if (!Number.isSafeInteger(record.seq) || Number(record.seq) < 1 || !string(record.id) ||
      !string(record.at) || !optional(string)(record.room) || !principal(record.from) || !body?.(record.body)) {
    return skip(value);
  }
  // The validators above cover every member of the generated Event union.
  return value as Event;
}

function skip(value: unknown): undefined {
  console.warn("Skipped an event that does not match the contract", value);
  return undefined;
}
