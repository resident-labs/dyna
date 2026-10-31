import type { Attachment, BoxState, Card, Conversation, Event, FailureCause, Principal } from "./contract.gen.ts";
import type { BoxFailure } from "./status.ts";
import type { Request } from "./room.svelte.ts";

export type Row =
  | { kind: "post"; key: string; from: Principal; at: string; text: string; attachments: Attachment[]; first: boolean; to: Principal[]; pending?: Pending }
  | { kind: "restarted"; key: string; session: string; at: string }
  | { kind: "failed"; key: string; seq: number; session: string; at: string; label: "Turn failed" | "Failed" | "Could not start" | "Could not close" | "Stopped unexpectedly"; cause: string }
  | { kind: "work"; key: string; events: Event[]; until?: string }
  | { kind: "request"; key: string; request: Request }
  | { kind: "day"; key: string; at: string }
  | { kind: "new"; key: string };

// An owner's post the engine has not confirmed yet, with its mentions as sessions.
export type Pending = { id: number; at: string; text: string; attachments: Attachment[]; to: Principal[]; failed: boolean };

type Name = (session: string) => string;

// Why a box or a request failed, in the owner's words. The engine's reason and error texts are raw (r8), so they never show.
// Why a failed or lost box stopped, to follow "Failed". A box's cause is known from its event; without it, the state says what it can.
export const boxReason = ({ box, cause }: BoxFailure): string => {
  const reason = cause ? causes[cause] : box === "lost" ? "stopped unexpectedly" : "could not start";
  return reason[0].toLowerCase() + reason.slice(1);
};

// Why a session's last turn failed, to follow "failed".
export const turnReason = (cause?: FailureCause): string => {
  const reason = cause ? causes[cause] : "the last turn failed";
  return reason[0].toLowerCase() + reason.slice(1);
};

// A box's failed row, in the feed and at the end of a DM. A stopped box with a cause may still start, so it has not failed.
export const boxRow = (box: BoxState, cause?: FailureCause) => ({
  label: cause === "remove_failed" || cause === "archive_failed" ? "Could not close" as const : box === "lost" ? "Stopped unexpectedly" as const : box === "failed" ? "Failed" as const : "Could not start" as const,
  cause: cause ? causes[cause] : box === "failed" ? "Could not start" : "",
});

export const causes: Record<FailureCause, string> = {
  create_failed: "Its workspace could not be created on the host",
  start_failed: "Its workspace could not start on the host",
  no_hello: "Its workspace did not connect in time",
  engine_restarted: "Dyna restarted while it set up the workspace",
  remove_failed: "Dyna could not remove its workspace",
  archive_failed: "Dyna could not save the session's branch",
  grant_inactive: "The host execution grant expired or was revoked",
  host_lost: "The host stopped answering during the run",
  host_busy: "host busy, try again later",
  github_failed: "GitHub refused the request or did not answer",
  stale: "Something it depended on changed before it ran",
  interrupted: "Dyna stopped during the run, so the outcome is unknown",
  internal: "Dyna had an internal error",
  setup_failed: "Its workspace could not set up the repository",
  model_failed: "The model provider refused or failed the request",
  output_limit: "The reply reached the model's output limit",
  harness_failed: "The agent program stopped unexpectedly",
  set_model_failed: "The session could not switch to its new model",
};

const sameHome = (a: Conversation, b: Conversation) => a.kind === b.kind && (a.session ?? "") === (b.session ?? "");
const sameAuthor = (a: Principal, b: Principal) => a.kind === b.kind && a.session === b.session;

// The session an event is about; events without one belong to the room feed only.
function subject(event: Event): string | undefined {
  switch (event.type) {
    case "host.disk_low": case "conversation.read": case "post.created": case "image.built": case "image.failed": case "box.pressure": case "grant.expired": case "rule.added": case "rule.removed": case "rule.edited": case "session.profile_changed": return undefined;
    case "op.requested": return event.body.by.session;
    case "op.decided": case "op.ran": return event.from.session;
    default: return event.body.session;
  }
}

// Builds the rows of one conversation. Posts and final answers become post rows, a
// failed turn and a request are each their own row, and all other events between them fold into one work row.
// A request's decision and run update its row, so they never show twice.
// Pending owner posts follow as post rows, so they group like the posts that replace them.
// A day row comes before the first row of each local day, so a new day also starts a new name line.
// With the owner's read position (since), a New row comes before the first later post or failed row by someone else.
export function feedRows(events: Event[], home: Conversation, pending: Pending[] = [], since = 0): Row[] {
  const rows: Row[] = [];
  let work: Event[] = [];
  let today = "";
  const requests = new Map<string, Extract<Row, { kind: "request" }>>();
  const dated = (at: string) => {
    const day = new Date(at).toDateString();
    if (day !== today) rows.push({ kind: "day", key: `d${day}`, at });
    today = day;
  };
  let unread = since > 0;
  const fresh = (seq: number, from: Principal) => {
    if (unread && seq > since && from.kind !== "owner") { rows.push({ kind: "new", key: "new" }); unread = false; }
  };
  // until is the end of a turn that answers or fails in its own row, so the work row above it still knows when the work ended.
  const flush = (until?: string) => {
    if (work.length) { dated(work[0].at); rows.push({ kind: "work", key: `w${work[0].seq}`, events: work, ...until && { until } }); }
    work = [];
  };
  // A post to other people, its mentions or a final reply's target, starts a new run with its own name line.
  const post = (key: string, seq: number, from: Principal, at: string, text: string, attachments: Attachment[], to: Principal[] = [], pending?: Pending) => {
    flush();
    dated(at);
    fresh(seq, from);
    const previous = rows.at(-1);
    const first = previous?.kind !== "post" || !sameAuthor(previous.from, from) ||
      previous.to.length !== to.length || previous.to.some((target, i) => !sameAuthor(target, to[i]));
    rows.push({ kind: "post", key, from, at, text, attachments, first, to, ...pending && { pending } });
  };
  for (const event of events) {
    if (event.type === "post.created") {
      if (sameHome(event.body.home, home)) post(`p${event.seq}`, event.seq, event.from, event.at, event.body.text, event.body.attachments ?? [], event.body.mentions ?? []);
      continue;
    }
    // A decision can come from a session outside a DM, so it updates the request's row before the DM filter.
    if (event.type === "op.decided" || event.type === "op.ran") {
      const row = requests.get(event.body.id);
      if (row) row.request = { ...row.request, ...event.body };
      continue;
    }
    const session = subject(event);
    if (home.kind === "dm" && session !== home.session) continue;
    if (event.type === "op.requested") {
      flush();
      dated(event.at);
      const row = { kind: "request" as const, key: `r${event.seq}`, request: { ...event.body, state: "pending" as const } };
      requests.set(event.body.id, row);
      rows.push(row);
      continue;
    }
    // Box lifecycle stays out of the feed; only a failed or lost box, or a close that failed to archive,
    // shows as a failed row with its cause in the owner's words.
    if (event.type === "session.restarted") {
      flush(); dated(event.at);
      rows.push({ kind: "restarted", key: `restart${event.seq}`, session: event.body.session, at: event.at });
      continue;
    }
    if (event.type === "box.changed") {
      const { to, cause } = event.body;
      // A lost box that then fails has said why already.
      if ((to === "failed" || to === "lost" || cause) && !(to === "failed" && event.body.from === "lost")) {
        flush();
        dated(event.at);
        fresh(event.seq, { kind: "session", session: event.body.session });
        rows.push({ kind: "failed", key: `f${event.seq}`, seq: event.seq, session: event.body.session, at: event.at, ...boxRow(to, cause) });
      }
      continue;
    }
    if (event.type === "turn.ended") {
      // A final reply that no post of the session already carried is the session's answer here.
      const answer = event.body.final && (event.body.replies ?? []).find(({ reply, posted }) =>
        reply.kind === "final" && !posted && sameHome(reply.home ?? { kind: "feed" }, home));
      // Only the end of a turn whose start the row holds ends the row's time.
      const turn = event.body.turn;
      if ((event.body.final && answer) || event.body.outcome === "failed") {
        const until = work.some(e => e.type === "turn.started" && e.body.turn === turn) ? event.at : undefined;
        // A reloaded DM window holds only posts and the turns that answered in it, so this end is the turn's only event.
        // The work row keeps it, so the turn's steps can still open above its answer.
        if (home.kind === "dm") work.push(event);
        flush(until);
      }
      if (event.body.final && answer) {
        post(`a${event.seq}`, event.seq, { kind: "session", session: event.body.session }, event.at, event.body.final, [], answer.reply.target ? [answer.reply.target] : []);
      }
      // A failed turn always gets its own row, even after its answer; it never folds into a work row. Its error is the
      // harness's raw text (r8), so the row shows only the owner words for its typed cause.
      if (event.body.outcome === "failed") {
        flush();
        dated(event.at);
        fresh(event.seq, { kind: "session", session: event.body.session });
        const cause = event.body.cause;
        rows.push({ kind: "failed", key: `f${event.seq}`, seq: event.seq, session: event.body.session, at: event.at, label: "Turn failed", cause: cause ? causes[cause] : "" });
      }
      if (answer || event.body.outcome === "failed") continue;
    }
    work.push(event);
  }
  for (const sent of pending) post(`n${sent.id}`, 0, { kind: "owner" }, sent.at, sent.text, sent.attachments, sent.to, sent);
  flush();
  return rows;
}

// A full moment for a title, such as "Oct 31, 2026, 4:48 AM".
export const fullTime = (value: string): string => new Date(value).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });

// The muted time on a row's name line: the clock today, else the date and the clock.
export function formatTime(value: string): string {
  const at = new Date(value);
  const clock = at.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return at.toDateString() === new Date().toDateString() ? clock : `${at.toLocaleDateString([], { month: "short", day: "numeric" })}, ${clock}`;
}

// A post the owner forwards, as the composer holds it until a send or a remove.
export type Quote = { author: string; at: string; text: string; attachments: Attachment[] };

// As in v1, a forward quotes the post above the owner's words. It travels as a markdown blockquote, so the feed
// draws it as a quote and the session that reads the post sees the quoted text. The empty quoted line keeps the
// label its own paragraph, so a quoted list or table still renders. Every line break, a lone CR too, starts a quoted line.
export function quoted({ author, at, text }: Quote, note: string): string {
  const lines = [`**Forwarded from ${author}** · ${formatTime(at)}`, "", ...text.split(/\r\n|\r|\n/)];
  return lines.map(line => line ? `> ${line}` : ">").join("\n") + (note.trim() ? `\n\n${note.trim()}` : "");
}

// A day row's label: "Today", "Yesterday", else the weekday and date, with the year only when it is not this year.
export function dayLabel(value: string, now: number): string {
  const at = new Date(value), day = new Date(now);
  if (at.toDateString() === day.toDateString()) return "Today";
  day.setDate(day.getDate() - 1);
  if (at.toDateString() === day.toDateString()) return "Yesterday";
  return at.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric", ...at.getFullYear() !== day.getFullYear() && { year: "numeric" } });
}

// A muted time from now, such as "in 59 minutes" or "2 minutes ago".
const relative = new Intl.RelativeTimeFormat([], { numeric: "auto" });
export function fromNow(value: string, now: number): string {
  const minutes = Math.round((new Date(value).getTime() - now) / 60_000);
  if (minutes === 0) return new Date(value).getTime() > now ? "in under a minute" : "just now";
  if (Math.abs(minutes) < 60) return relative.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  return Math.abs(hours) < 24 ? relative.format(hours, "hour") : relative.format(Math.round(hours / 24), "day");
}

// A command line the owner can read back exactly: an argument with anything but plain
// characters, or an empty one, is single-quoted as a POSIX shell would read it.
export function commandLine(argv: string[]): string {
  return argv.map(arg => /^[\w@%+=:,./-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", `'\\''`)}'`).join(" ");
}

// A request's first line, split around the host or repository it names, so that name shows in mono. named is empty when
// the line does not name it.
export function requestLine(card: Pick<Card, "body" | "target">): { text: string; named: string; after: string } {
  const text = card.body?.split("\n")[0] ?? "", name = card.target.kind === "room" ? "" : card.target.name;
  const at = name ? text.indexOf(name) : -1;
  return at < 0 ? { text, named: "", after: "" } : { text: text.slice(0, at), named: name, after: text.slice(at + name.length) };
}

// Files the browser shows inline as a thumbnail; other types get a file card.
export const isImage = (file: Attachment) => /^image\/(png|jpeg|gif|webp)$/.test(file.media);

// A file size as v1 shows it, such as "512 B", "12.4 KB" or "3.1 MB".
export function fileSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

// A long path in an activity label keeps its first folder and its last two parts, so the file name always shows:
// "editing next/engine/approve/store/files.go" becomes "editing next/…/store/files.go".
export const shortPaths = (label: string) => label.replace(/\S+(?:\/\S+){4,}/g, path => {
  const parts = path.split("/");
  return [parts[0], "…", ...parts.slice(-2)].join("/");
});

export const count = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

// A session the room no longer lists.
export const closedSession = "A closed session";
const list = (parts: string[]) => parts.length < 2 ? parts.join("") : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;

// One short line per work row in the owner's terms, such as "lead-web worked for 6 min · 2 turns · 1 job" or
// "builder-feed started · 3 sessions worked for 12 min · 4 turns"; the opened row lists each event.
// The time runs from the first turn's start to the last turn's end in the row, in whole minutes as T3 Code's
// "Worked for 20s"; without both ends the line says "lead-web worked · 2 turns". A DM's saved steps give the start
// that a reloaded window lacks.
export function workSummary(events: Event[], name: Name, until?: string, from?: string): string {
  const started: string[] = [], moved: string[] = [], closed: string[] = [], opened: string[] = [], shut: string[] = [];
  const turns = new Map<string, Set<string>>();
  let jobs = 0, images = 0, failedImages = 0, prs = 0, memory = 0, grants = 0, rules = 0, first = Infinity, last = -Infinity;
  for (const event of events) {
    switch (event.type) {
      case "session.started": started.push(name(event.body.session)); break;
      case "session.moved": moved.push(name(event.body.session)); break;
      case "session.closed": closed.push(name(event.body.session)); break;
      case "computer.changed": (event.body.to === "running" ? opened : shut).push(name(event.body.session)); break;
      case "turn.started": case "turn.ended":
        turns.set(event.body.session, (turns.get(event.body.session) ?? new Set()).add(event.body.turn));
        if (event.type === "turn.started") first = Math.min(first, Date.parse(event.at)); else last = Math.max(last, Date.parse(event.at));
        break;
      case "shell.job.ended": jobs++; break;
      case "image.built": images++; break;
      case "image.failed": failedImages++; break;
      case "pr.opened": prs++; break;
      case "box.pressure": memory++; break;
      case "grant.expired": grants++; break;
      case "rule.added": case "rule.removed": case "rule.edited": rules++; break;
    }
  }
  if (until) last = Math.max(last, Date.parse(until));
  if (from) first = Math.min(first, Date.parse(from));
  const who = (sessions: string[]) => sessions.length > 2 ? `${sessions.length} sessions`
    : sessions.length === 2 && sessions.every(session => session === closedSession) ? "Closed sessions" : list([...new Set(sessions)]);
  const minutes = Math.round((last - first) / 60_000);
  const took = minutes < 1 ? "under a minute" : minutes < 60 ? `${minutes} min` : `${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} min` : ""}`;
  const ran = [...turns.values()].reduce((sum, set) => sum + set.size, 0);
  const parts = [
    started.length && `${who(started)} started`,
    turns.size && `${who([...turns.keys()].map(name))} worked${last >= first ? ` for ${took}` : ""}`,
    ran && count(ran, "turn"), jobs && count(jobs, "job"), images && `${count(images, "image")} built`,
    failedImages && `${count(failedImages, "image build")} failed`, prs && count(prs, "pull request"),
    memory && count(memory, "memory warning"), grants && `${count(grants, "host execution grant")} expired`,
    rules && count(rules, "rule change"),
    opened.length && `${who(opened)} opened a computer`, shut.length && `${who(shut)} closed a computer`, moved.length && `${who(moved)} moved`, closed.length && `${who(closed)} closed`,
  ].filter(part => typeof part === "string");
  return parts.length ? parts.join(" · ") : count(events.length, "update");
}

// One plain line for an event inside an opened work row.
export function describe(event: Event, name: Name): string {
  switch (event.type) {
    case "session.restarted": return `${name(event.body.session)}'s box was replaced. Unpushed work in the old volume is gone.`;
    case "session.started": return `${name(event.body.session)} started`;
    case "session.closed": return `${name(event.body.session)} closed`;
    case "session.moved": return `${name(event.body.session)} moved under ${name(event.body.newParent)}`;
    case "computer.changed": return `${name(event.body.session)} ${event.body.to === "running" ? "opened" : "closed"} its computer`;
    case "box.changed": return ""; // feedRows keeps box changes out of work rows.
    case "turn.started": return `${name(event.body.session)} started a turn`;
    case "turn.ended": return event.body.outcome === "stopped" ? `${name(event.body.session)}'s turn stopped` : `${name(event.body.session)} finished a turn`;
    // Live only: the room store keeps it out of every feed window.
    case "host.disk_low": case "conversation.read": return "";
    case "turn.activity": return event.body.label;
    case "shell.job.ended": return event.body.exitCode === 0 ? `${name(event.body.session)}'s job finished` : `${name(event.body.session)}'s job failed with exit code ${event.body.exitCode}`;
    case "op.requested": case "op.decided": case "op.ran": return ""; // feedRows gives each request its own row.
    case "image.built": return `Image ${event.body.name}:${event.body.tag} built in ${event.body.seconds} s`;
    // The reason is raw build output (r8); the build log holds it.
    case "image.failed": return `Image ${event.body.name}:${event.body.tag} failed`;
    case "pr.opened": return `${name(event.body.session)} opened pull request ${event.body.repo}#${event.body.number}`;
    case "grant.expired": return "A host execution grant expired";
    case "rule.added": return `Rule r${event.body.number} added: ${event.body.text}`;
    case "rule.removed": return `Rule r${event.body.number} removed`;
    case "rule.edited": return `Rule r${event.body.number} changed: ${event.body.text}`;
    case "session.profile_changed": return ""; // feedRows shows no row for it.
    case "box.pressure": return event.body.kind === "max" ? "A session reached its memory limit" : "A session is short of memory";
    case "post.created": return event.body.text;
  }
}
