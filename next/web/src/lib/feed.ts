import type { Attachment, BoxState, Conversation, Event, Principal } from "./contract.gen.ts";

export type Row =
  | { kind: "post"; key: string; from: Principal; at: string; text: string; attachments: Attachment[]; first: boolean; to: Principal[] }
  | { kind: "failed"; key: string; session: string; at: string; error: string }
  | { kind: "work"; key: string; events: Event[] };

type Name = (session: string) => string;

const sameHome = (a: Conversation, b: Conversation) => a.kind === b.kind && (a.session ?? "") === (b.session ?? "");
const sameAuthor = (a: Principal, b: Principal) => a.kind === b.kind && a.session === b.session;

// The session an event is about; events without one belong to the room feed only.
function subject(event: Event): string | undefined {
  switch (event.type) {
    case "post.created": case "image.built": case "image.failed": case "box.pressure": case "grant.expired": return undefined;
    case "op.requested": return event.body.by.session;
    case "op.decided": case "op.ran": return event.from.session;
    default: return event.body.session;
  }
}

// Builds the rows of one conversation. Posts and final answers become post rows, a
// failed turn is its own row, and all other events between them fold into one work row.
export function feedRows(events: Event[], home: Conversation): Row[] {
  const rows: Row[] = [];
  let work: Event[] = [];
  const flush = () => {
    if (work.length) rows.push({ kind: "work", key: `w${work[0].seq}`, events: work });
    work = [];
  };
  // A post to other people, its mentions or a final reply's target, starts a new run with its own name line.
  const post = (key: string, from: Principal, at: string, text: string, attachments: Attachment[], to: Principal[] = []) => {
    flush();
    const previous = rows.at(-1);
    const first = previous?.kind !== "post" || !sameAuthor(previous.from, from) ||
      previous.to.length !== to.length || previous.to.some((target, i) => !sameAuthor(target, to[i]));
    rows.push({ kind: "post", key, from, at, text, attachments, first, to });
  };
  for (const event of events) {
    if (event.type === "post.created") {
      if (sameHome(event.body.home, home)) post(`p${event.seq}`, event.from, event.at, event.body.text, event.body.attachments ?? [], event.body.mentions ?? []);
      continue;
    }
    const session = subject(event);
    if (home.kind === "dm" && session !== home.session) continue;
    if (event.type === "turn.ended") {
      // A final reply that no post of the session already carried is the session's answer here.
      const answer = event.body.final && (event.body.replies ?? []).find(({ reply, posted }) =>
        reply.kind === "final" && !posted && sameHome(reply.home ?? { kind: "feed" }, home));
      if (event.body.final && answer) {
        post(`a${event.seq}`, { kind: "session", session: event.body.session }, event.at, event.body.final, [], answer.reply.target ? [answer.reply.target] : []);
        continue;
      }
      if (event.body.outcome === "failed") {
        flush();
        rows.push({ kind: "failed", key: `f${event.seq}`, session: event.body.session, at: event.at, error: event.body.error ?? "" });
        continue;
      }
    }
    work.push(event);
  }
  flush();
  return rows;
}

// The muted time on a row's name line: the clock today, else the date and the clock.
export function formatTime(value: string): string {
  const at = new Date(value);
  const clock = at.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return at.toDateString() === new Date().toDateString() ? clock : `${at.toLocaleDateString([], { month: "short", day: "numeric" })}, ${clock}`;
}

const count = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

// What a box change means in a work row's sentence.
const boxPhrase: Record<BoxState, string> = {
  pending: "is starting its box", creating: "is starting its box", starting: "is starting its box", ready: "started its box",
  stopped: "stopped its box", closing: "closed its box", deleted: "closed its box", failed: "had its box fail", lost: "lost its box",
};
const list = (parts: string[]) => parts.length < 2 ? parts.join("") : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;

// One quiet sentence per work row, such as "lead-web finished 2 turns and started its box".
// Several sessions read as "root, lead-web and builder-feed worked"; the opened row lists each event.
export function workSummary(events: Event[], name: Name): string {
  const groups = new Map<string, {
    begun: number; turns: number; jobs: number; operations: Set<string>; images: number; failedImages: number; prs: number; pressure: number; grants: number;
    box?: BoxState; started: boolean; closed: boolean;
  }>();
  for (const event of events) {
    const session = subject(event);
    const label = session ? name(session) : "Dyna";
    const group = groups.get(label) ?? {
      begun: 0, turns: 0, jobs: 0, operations: new Set<string>(), images: 0, failedImages: 0, prs: 0, pressure: 0, grants: 0, started: false, closed: false,
    };
    groups.set(label, group);
    switch (event.type) {
      case "session.started": group.started = true; break;
      case "session.closed": group.closed = true; break;
      case "box.changed": group.box = event.body.to; break;
      case "turn.started": group.begun++; break;
      case "turn.ended": group.turns++; break;
      case "shell.job.ended": group.jobs++; break;
      case "op.requested": case "op.decided": case "op.ran": group.operations.add(event.body.id); break;
      case "image.built": group.images++; break;
      case "image.failed": group.failedImages++; break;
      case "pr.opened": group.prs++; break;
      case "box.pressure": group.pressure++; break;
      case "grant.expired": group.grants++; break;
    }
  }
  const labels = [...groups.keys()];
  if (labels.length > 1) return `${list(labels.length > 3 ? [...labels.slice(0, 2), `${labels.length - 2} more`] : labels)} worked`;
  const [label, group] = [...groups][0];
  const parts = [
    group.started && "started", !group.turns && group.begun && `started ${count(group.begun, "turn")}`, group.turns && `finished ${count(group.turns, "turn")}`, group.jobs && `ran ${count(group.jobs, "job")}`,
    group.operations.size && `made ${count(group.operations.size, "request")}`, group.images && `built ${count(group.images, "image")}`,
    group.failedImages && `had ${count(group.failedImages, "image build")} fail`,
    group.prs && `opened ${count(group.prs, "pull request")}`, group.pressure && `had ${count(group.pressure, "memory warning")}`,
    group.grants && `had ${count(group.grants, "host-exec grant")} expire`, group.box && boxPhrase[group.box], group.closed && "closed",
  ].filter(part => typeof part === "string");
  return `${label} ${parts.length ? list(parts) : "worked"}`;
}

// One plain line for an event inside an opened work row.
export function describe(event: Event, name: Name): string {
  const who = (principal: Principal) => principal.kind === "owner" ? "You" : principal.session ? name(principal.session) : "Dyna";
  switch (event.type) {
    case "session.started": return `${name(event.body.session)} started`;
    case "session.closed": return `${name(event.body.session)} closed`;
    case "session.moved": return `${name(event.body.session)} moved under ${name(event.body.newParent)}`;
    case "box.changed": return `${name(event.body.session)}'s box is ${event.body.to}`;
    case "turn.started": return `${name(event.body.session)} started a turn`;
    case "turn.ended": return event.body.outcome === "stopped" ? `${name(event.body.session)}'s turn stopped` : `${name(event.body.session)} finished a turn`;
    // Live only: the room store keeps it out of every feed window.
    case "turn.activity": return event.body.label;
    case "shell.job.ended": return `${name(event.body.session)}'s job ended with exit code ${event.body.exitCode}`;
    case "op.requested": return `${who(event.body.by)} asked to ${event.body.card.title}`;
    case "op.decided": return `A request was ${event.body.state}`;
    case "op.ran": return event.body.state === "done" ? "A request ran" : `A request ${event.body.state}`;
    case "image.built": return `Image ${event.body.name}:${event.body.tag} built in ${event.body.seconds} s`;
    case "image.failed": return `Image ${event.body.name}:${event.body.tag} failed: ${event.body.reason}`;
    case "pr.opened": return `${name(event.body.session)} opened pull request ${event.body.repo}#${event.body.number}`;
    case "grant.expired": return "A host-exec grant expired";
    case "box.pressure": return event.body.kind === "max" ? "A box reached its memory limit" : "A box is short of memory";
    case "post.created": return event.body.text;
  }
}
