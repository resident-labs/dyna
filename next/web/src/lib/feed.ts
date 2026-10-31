import type { Attachment, Conversation, Event, FailureCause, Principal } from "./contract.gen.ts";

export type Row =
  | { kind: "post"; key: string; from: Principal; at: string; text: string; attachments: Attachment[]; first: boolean; to: Principal[]; pending?: Pending }
  | { kind: "failed"; key: string; seq: number; session: string; at: string; label: "Turn failed" | "Could not start" | "Could not close" | "Stopped unexpectedly"; cause: string }
  | { kind: "work"; key: string; events: Event[] }
  | { kind: "day"; key: string; at: string }
  | { kind: "new"; key: string };

// An owner's post the engine has not confirmed yet, with its mentions as sessions.
export type Pending = { id: number; at: string; text: string; attachments: Attachment[]; to: Principal[]; failed: boolean };

type Name = (session: string) => string;

// Why a box or a request failed, in the owner's words. The engine's reason and error texts are raw (r8), so they never show.
export const causes: Record<FailureCause, string> = {
  create_failed: "Its workspace could not be created on the host",
  no_hello: "Its workspace did not connect in time",
  engine_restarted: "Dyna restarted while it set up the workspace",
  remove_failed: "Dyna could not remove its workspace",
  archive_failed: "Dyna could not save the session's branch",
  grant_inactive: "The host execution grant expired or was revoked",
  host_lost: "The host stopped answering during the run",
  github_failed: "GitHub refused the request or did not answer",
  stale: "Something it depended on changed before it ran",
  interrupted: "Dyna stopped during the run, so the outcome is unknown",
  internal: "Dyna had an internal error",
  setup_failed: "Its workspace could not set up the repository",
  model_failed: "The model provider refused or failed the request",
  output_limit: "The reply reached the model's output limit",
  harness_failed: "The agent program stopped unexpectedly",
};

const sameHome = (a: Conversation, b: Conversation) => a.kind === b.kind && (a.session ?? "") === (b.session ?? "");
const sameAuthor = (a: Principal, b: Principal) => a.kind === b.kind && a.session === b.session;

// The session an event is about; events without one belong to the room feed only.
function subject(event: Event): string | undefined {
  switch (event.type) {
    case "conversation.read": case "post.created": case "image.built": case "image.failed": case "box.pressure": case "grant.expired": case "rule.added": case "rule.removed": return undefined;
    case "op.requested": return event.body.by.session;
    case "op.decided": case "op.ran": return event.from.session;
    default: return event.body.session;
  }
}

// Builds the rows of one conversation. Posts and final answers become post rows, a
// failed turn is its own row, and all other events between them fold into one work row.
// Pending owner posts follow as post rows, so they group like the posts that replace them.
// A day row comes before the first row of each local day, so a new day also starts a new name line.
// With the owner's read position (since), a New row comes before the first later post or failed row by someone else.
export function feedRows(events: Event[], home: Conversation, pending: Pending[] = [], since = 0): Row[] {
  const rows: Row[] = [];
  let work: Event[] = [];
  let today = "";
  const dated = (at: string) => {
    const day = new Date(at).toDateString();
    if (day !== today) rows.push({ kind: "day", key: `d${day}`, at });
    today = day;
  };
  let unread = since > 0;
  const fresh = (seq: number, from: Principal) => {
    if (unread && seq > since && from.kind !== "owner") { rows.push({ kind: "new", key: "new" }); unread = false; }
  };
  const flush = () => {
    if (work.length) { dated(work[0].at); rows.push({ kind: "work", key: `w${work[0].seq}`, events: work }); }
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
    const session = subject(event);
    if (home.kind === "dm" && session !== home.session) continue;
    // Box lifecycle stays out of the feed; only a failed or lost box, or a close that failed to archive,
    // shows as a failed row with its cause in the owner's words.
    if (event.type === "box.changed") {
      const { to, cause } = event.body;
      if (to === "failed" || to === "lost" || cause) {
        flush();
        dated(event.at);
        fresh(event.seq, { kind: "session", session: event.body.session });
        rows.push({ kind: "failed", key: `f${event.seq}`, seq: event.seq, session: event.body.session, at: event.at, cause: cause ? causes[cause] : "",
          label: cause === "remove_failed" || cause === "archive_failed" ? "Could not close" : to === "lost" ? "Stopped unexpectedly" : "Could not start" });
      }
      continue;
    }
    if (event.type === "turn.ended") {
      // A final reply that no post of the session already carried is the session's answer here.
      const answer = event.body.final && (event.body.replies ?? []).find(({ reply, posted }) =>
        reply.kind === "final" && !posted && sameHome(reply.home ?? { kind: "feed" }, home));
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

// Files the browser shows inline as a thumbnail; other types get a file card.
export const isImage = (file: Attachment) => /^image\/(png|jpeg|gif|webp)$/.test(file.media);

// A file size as v1 shows it, such as "512 B", "12.4 KB" or "3.1 MB".
export function fileSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export const count = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

const list = (parts: string[]) => parts.length < 2 ? parts.join("") : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;

// One short line per work row in the owner's terms, such as "lead-web ran 2 turns" or
// "builder-feed started, 3 sessions ran turns, 1 job"; the opened row lists each event.
export function workSummary(events: Event[], name: Name): string {
  const started: string[] = [], moved: string[] = [], closed: string[] = [], opened: string[] = [], shut: string[] = [];
  const turns = new Map<string, Set<string>>();
  const requests = new Set<string>();
  let jobs = 0, images = 0, failedImages = 0, prs = 0, memory = 0, grants = 0, rules = 0;
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
      case "rule.added": case "rule.removed": rules++; break;
    }
  }
  const who = (sessions: string[]) => sessions.length > 2 ? `${sessions.length} sessions` : list(sessions);
  const [ran] = turns;
  const parts = [
    started.length && `${who(started)} started`,
    turns.size > 1 ? `${who([...turns.keys()].map(name))} ran turns` : ran && `${name(ran[0])} ran ${count(ran[1].size, "turn")}`,
    jobs && count(jobs, "job"), requests.size && count(requests.size, "request"), images && `${count(images, "image")} built`,
    failedImages && `${count(failedImages, "image build")} failed`, prs && count(prs, "pull request"),
    memory && count(memory, "memory warning"), grants && `${count(grants, "host execution grant")} expired`,
    rules && count(rules, "rule change"),
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
    case "conversation.read": return "";
    case "turn.activity": return event.body.label;
    case "shell.job.ended": return event.body.exitCode === 0 ? `${name(event.body.session)}'s job finished` : `${name(event.body.session)}'s job failed with exit code ${event.body.exitCode}`;
    case "op.requested": return `${who(event.body.by)} asked to ${event.body.card.title}`;
    case "op.decided": return `A request was ${event.body.state}`;
    case "op.ran": {
      const { state, result: { cause } } = event.body;
      return state === "done" ? "A request ran" : cause ? `A request ${state}: ${causes[cause]}` : `A request ${state}`;
    }
    case "image.built": return `Image ${event.body.name}:${event.body.tag} built in ${event.body.seconds} s`;
    // The reason is raw build output (r8); the build log holds it.
    case "image.failed": return `Image ${event.body.name}:${event.body.tag} failed`;
    case "pr.opened": return `${name(event.body.session)} opened pull request ${event.body.repo}#${event.body.number}`;
    case "grant.expired": return "A host execution grant expired";
    case "rule.added": return `Rule r${event.body.number} added: ${event.body.text}`;
    case "rule.removed": return `Rule r${event.body.number} removed`;
    case "box.pressure": return event.body.kind === "max" ? "A session reached its memory limit" : "A session is short of memory";
    case "post.created": return event.body.text;
  }
}
