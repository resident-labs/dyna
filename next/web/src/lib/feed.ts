import type { Attachment, Conversation, Event, Principal } from "./contract.gen.ts";

export type Row =
  | { kind: "post"; key: string; from: Principal; at: string; text: string; attachments: Attachment[]; first: boolean; to: Principal[] }
  | { kind: "failed"; key: string; session: string; at: string; label: "Turn failed" | "Could not start" | "Stopped unexpectedly"; error: string }
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
    // Box lifecycle stays out of the feed; only a failed or lost box shows, as a failed row in the owner's
    // terms. Its reason is raw engine error text, so it never shows.
    if (event.type === "box.changed") {
      if (event.body.to === "failed" || event.body.to === "lost") {
        flush();
        rows.push({ kind: "failed", key: `f${event.seq}`, session: event.body.session, at: event.at,
          label: event.body.to === "failed" ? "Could not start" : "Stopped unexpectedly", error: "" });
      }
      continue;
    }
    if (event.type === "turn.ended") {
      // A final reply that no post of the session already carried is the session's answer here.
      const answer = event.body.final && (event.body.replies ?? []).find(({ reply, posted }) =>
        reply.kind === "final" && !posted && sameHome(reply.home ?? { kind: "feed" }, home));
      if (event.body.final && answer) {
        post(`a${event.seq}`, { kind: "session", session: event.body.session }, event.at, event.body.final, [], answer.reply.target ? [answer.reply.target] : []);
      }
      // A failed turn always gets its own row, even after its answer; it never folds into a work row.
      if (event.body.outcome === "failed") {
        flush();
        rows.push({ kind: "failed", key: `f${event.seq}`, session: event.body.session, at: event.at, label: "Turn failed", error: event.body.error ?? "" });
      }
      if (answer || event.body.outcome === "failed") continue;
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

// A muted time from now, such as "in 59 minutes" or "2 minutes ago".
const relative = new Intl.RelativeTimeFormat([], { numeric: "auto" });
export function fromNow(value: string, now: number): string {
  const minutes = Math.round((new Date(value).getTime() - now) / 60_000);
  if (minutes === 0) return "just now";
  if (Math.abs(minutes) < 60) return relative.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  return Math.abs(hours) < 24 ? relative.format(hours, "hour") : relative.format(Math.round(hours / 24), "day");
}

// A command line the owner can read back exactly: an argument with anything but plain
// characters, or an empty one, is single-quoted as a POSIX shell would read it.
export function commandLine(argv: string[]): string {
  return argv.map(arg => /^[\w@%+=:,./-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", `'\\''`)}'`).join(" ");
}

export const count = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

const list = (parts: string[]) => parts.length < 2 ? parts.join("") : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;

// One short line per work row in the owner's terms, such as "lead-web ran 2 turns" or
// "builder-feed started, 3 sessions ran turns, 1 job"; the opened row lists each event.
export function workSummary(events: Event[], name: Name): string {
  const started: string[] = [], moved: string[] = [], closed: string[] = [], opened: string[] = [], shut: string[] = [];
  const turns = new Map<string, Set<string>>();
  const requests = new Set<string>();
  let jobs = 0, images = 0, failedImages = 0, prs = 0, memory = 0, grants = 0;
  for (const event of events) {
    switch (event.type) {
      case "session.started": started.push(name(event.body.session)); break;
      case "session.moved": moved.push(name(event.body.session)); break;
      case "session.closed": closed.push(name(event.body.session)); break;
      case "computer.changed": (event.body.to === "running" ? opened : shut).push(name(event.body.session)); break;
      case "turn.started": case "turn.ended": turns.set(event.body.session, (turns.get(event.body.session) ?? new Set()).add(event.body.turn)); break;
      case "shell.job.ended": jobs++; break;
      case "op.requested": case "op.decided": case "op.ran": requests.add(event.body.id); break;
      case "image.built": images++; break;
      case "image.failed": failedImages++; break;
      case "pr.opened": prs++; break;
      case "box.pressure": memory++; break;
      case "grant.expired": grants++; break;
    }
  }
  const who = (sessions: string[]) => sessions.length > 2 ? `${sessions.length} sessions` : list(sessions);
  const [ran] = turns;
  const parts = [
    started.length && `${who(started)} started`,
    turns.size > 1 ? `${who([...turns.keys()].map(name))} ran turns` : ran && `${name(ran[0])} ran ${count(ran[1].size, "turn")}`,
    jobs && count(jobs, "job"), requests.size && count(requests.size, "request"), images && `${count(images, "image")} built`,
    failedImages && `${count(failedImages, "image build")} failed`, prs && count(prs, "pull request"),
    memory && count(memory, "memory warning"), grants && `${count(grants, "host-exec grant")} expired`,
    opened.length && `${who(opened)} opened a computer`, shut.length && `${who(shut)} closed a computer`, moved.length && `${who(moved)} moved`, closed.length && `${who(closed)} closed`,
  ].filter(part => typeof part === "string");
  return parts.length ? parts.join(", ") : count(events.length, "update");
}

// One plain line for an event inside an opened work row.
export function describe(event: Event, name: Name): string {
  const who = (principal: Principal) => principal.kind === "owner" ? "You" : principal.session ? name(principal.session) : "Dyna";
  switch (event.type) {
    case "session.started": return `${name(event.body.session)} started`;
    case "session.closed": return `${name(event.body.session)} closed`;
    case "session.moved": return `${name(event.body.session)} moved under ${name(event.body.newParent)}`;
    case "computer.changed": return `${name(event.body.session)} ${event.body.to === "running" ? "opened" : "closed"} its computer`;
    case "box.changed": return ""; // feedRows keeps box changes out of work rows.
    case "turn.started": return `${name(event.body.session)} started a turn`;
    case "turn.ended": return event.body.outcome === "stopped" ? `${name(event.body.session)}'s turn stopped` : `${name(event.body.session)} finished a turn`;
    // Live only: the room store keeps it out of every feed window.
    case "turn.activity": return event.body.label;
    case "shell.job.ended": return event.body.exitCode === 0 ? `${name(event.body.session)}'s job finished` : `${name(event.body.session)}'s job failed with exit code ${event.body.exitCode}`;
    case "op.requested": return `${who(event.body.by)} asked to ${event.body.card.title}`;
    case "op.decided": return `A request was ${event.body.state}`;
    case "op.ran": return event.body.state === "done" ? "A request ran" : `A request ${event.body.state}`;
    case "image.built": return `Image ${event.body.name}:${event.body.tag} built in ${event.body.seconds} s`;
    case "image.failed": return `Image ${event.body.name}:${event.body.tag} failed: ${event.body.reason}`;
    case "pr.opened": return `${name(event.body.session)} opened pull request ${event.body.repo}#${event.body.number}`;
    case "grant.expired": return "A host-exec grant expired";
    case "box.pressure": return event.body.kind === "max" ? "A session reached its memory limit" : "A session is short of memory";
    case "post.created": return event.body.text;
  }
}
