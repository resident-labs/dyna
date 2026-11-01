import type { Attachment, BoxState, Card, Conversation, Event, FailureCause, Principal, SessionCloseReason } from "./contract.gen.ts";
import type { BoxFailure } from "./status.ts";
import type { PendingReason } from "./event.ts";
import type { Request } from "./room.svelte.ts";
import { endCause, failed } from "./jobs.ts";

export type Row =
  | { kind: "post"; key: string; from: Principal; at: string; text: string; attachments: Attachment[]; first: boolean; to: Principal[]; pending?: Pending }
  | { kind: "restarted"; key: string; session: string; at: string }
  // A plain line for what the owner did to a session or the room; describe gives its words.
  | { kind: "notice"; key: string; event: Event; resumedAt?: string }
  | { kind: "failed"; key: string; seq: number; session: string; at: string; label: "Turn failed" | "Failed" | "Could not start" | "Could not close" | "Stopped unexpectedly"; cause: string }
  | { kind: "more-failures"; key: string; failures: Extract<Row, { kind: "failed" }>[] }
  | { kind: "work"; key: string; events: Event[]; failedSessions: string[]; until?: string }
  | { kind: "request"; key: string; at: string; request: Request }
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

// Why a pending box waits, in place of "Starting" on its session's line and in About.
export const waits: Record<PendingReason, string> = {
  cpu: "Waiting for CPU", memory: "Waiting for memory", disk: "Waiting for disk space", "room-limit": "Room limit",
  "no-ready-host": "Waiting for a host", "room-host": "No host takes this room", "invalid-size": "Unknown box size",
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
  grant_inactive: "The host execution grant is no longer active",
  host_lost: "The host stopped answering during the run",
  host_busy: "host busy, try again later",
  nested_device_missing: "The host lacks the TUN device needed for nested containers",
  kvm_device_missing: "The host lacks the KVM device",
  github_failed: "GitHub refused the request or did not answer",
  stale: "Something it depended on changed before it ran",
  interrupted: "Dyna stopped during the run, so the outcome is unknown",
  internal: "Dyna had an internal error",
  setup_failed: "Its workspace could not set up the repository",
  model_failed: "The model provider refused or failed the request",
  output_limit: "The reply reached the model's output limit",
  harness_failed: "The agent program stopped unexpectedly",
  set_model_failed: "The session could not switch to its new model",
  not_started: "It stopped before it began",
};

const sameHome = (a: Conversation, b: Conversation) => a.kind === b.kind && (a.session ?? "") === (b.session ?? "");
const sameAuthor = (a: Principal, b: Principal) => a.kind === b.kind && a.session === b.session;

// The session an event is about; events without one belong to the room feed only.
export function subject(event: Event): string | undefined {
  switch (event.type) {
    case "transcript.changed": case "connect.connector": case "account.failed": case "account.quota_low": case "room.paused": case "room.resumed": case "room.limits_changed": case "room.deleting": case "room.deleted": case "host.disk_low": case "conversation.read": case "post.created": case "image.built": case "image.failed": case "box.pressure": case "grant.expired": case "session.profile_changed": return undefined;
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
export function feedRows(events: Event[], home: Conversation, pending: Pending[] = [], since = 0, retainedRequests: ReadonlySet<string> = new Set()): Row[] {
  const rows: Row[] = [];
  let work: Event[] = [];
  let deferred: Row[] = [];
  const turns = new Map<string, { session: string; at: string; posted: boolean; significant: boolean; events: Event[] }>();
  const turnRequests = new Map<string, string>();
  let today = "";
  const requests = new Map<string, Extract<Row, { kind: "request" }>>();
  const decided = new Set(events.flatMap(event => event.type === "op.decided" ? [event.body.id] : []));
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
    if (work.length) { dated(work[0].at); rows.push({ kind: "work", key: `w${work[0].seq}`, events: work, failedSessions: deferred.flatMap(row => row.kind === "failed" ? [row.session] : []), ...until && { until } }); }
    work = [];
    let failures = 0;
    let more: Extract<Row, { kind: "more-failures" }> | undefined;
    for (const row of deferred) {
      if (row.kind === "notice") dated(row.event.at);
      else if (row.kind === "request" || row.kind === "restarted") dated(row.at);
      else if (row.kind === "failed") {
        dated(row.at); fresh(row.seq, { kind: "session", session: row.session });
        if (++failures > 5) {
          if (!more) { more = { kind: "more-failures", key: `more-${row.key}`, failures: [] }; rows.push(more); }
          more.failures.push(row);
          continue;
        }
      }
      rows.push(row);
    }
    deferred = [];
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
  // A post that carried a final reply goes to that reply's target too. The engine records this at the turn's end, later
  // than the post, so the targets are gathered first.
  const carried = new Map<string, Principal[]>();
  for (const event of events) {
    if (event.type !== "turn.ended") continue;
    for (const { reply, posted } of event.body.replies ?? []) {
      if (posted && reply.target) carried.set(posted, [...carried.get(posted) ?? [], reply.target]);
    }
  }
  for (const event of events) {
    // Account alerts are owner-only and never belong to a room feed.
    if (event.type === "account.failed" || event.type === "account.quota_low" || event.type === "host.disk_low") continue;
    const firstAt = work[0]?.at ?? (deferred[0]?.kind === "notice" ? deferred[0].event.at : deferred[0]?.kind === "failed" || deferred[0]?.kind === "request" || deferred[0]?.kind === "restarted" ? deferred[0].at : undefined);
    if (firstAt && new Date(firstAt).toDateString() !== new Date(event.at).toDateString()) flush();
    if (event.type === "post.created") {
      const mentions = event.body.mentions ?? [];
      const replied = (carried.get(event.id) ?? []).filter(target => !mentions.some(mention => sameAuthor(mention, target)));
      if (sameHome(event.body.home, home)) {
        for (const turn of turns.values()) if (turn.session === event.from.session) turn.posted = true;
        post(`p${event.seq}`, event.seq, event.from, event.at, event.body.text, event.body.attachments ?? [], [...mentions, ...replied]);
      }
      continue;
    }
    // A decision can come from a session outside a DM, so it updates the request's row before the DM filter.
    if (event.type === "op.decided" || event.type === "op.ran") {
      const turn = turns.get(turnRequests.get(event.body.id) ?? "");
      if (turn && event.type === "op.decided") { turn.events.push(event); continue; }
      const row = requests.get(event.body.id);
      if (row) row.request = { ...row.request, ...event.body };
      else if (event.type === "op.decided" && home.kind === "feed") work.push(event);
      continue;
    }
    const session = subject(event);
    if (event.type === "turn.started") {
      if (home.kind !== "dm" || session === home.session) turns.set(event.body.turn, { session: event.body.session, at: event.at, posted: false, significant: false, events: [event] });
      continue;
    }
    if (event.type === "shell.job.ended" || event.type === "op.requested") {
      const entry = [...turns].find(([, turn]) => turn.session === session);
      let claimed = false;
      if (entry) {
        const [id, turn] = entry;
        turn.significant = true;
        if (event.type === "shell.job.ended" || (decided.has(event.body.id) && !retainedRequests.has(event.body.id))) {
          turn.events.push(event);
          if (event.type === "op.requested") turnRequests.set(event.body.id, id);
          claimed = true;
        }
      }
      if (claimed) continue;
      // A job without an open turn stays in the work buffer below.
    }
    if (home.kind === "dm" && session !== home.session) continue;
    if (event.type === "room.paused" || event.type === "room.resumed" || event.type === "room.limits_changed" || event.type === "room.deleting") {
      const previous = deferred.at(-1);
      if (event.type === "room.resumed" && previous?.kind === "notice" && previous.event.type === "room.paused" && sameAuthor(previous.event.from, event.from) && Date.parse(event.at) - Date.parse(previous.event.at) <= 300_000) previous.resumedAt = event.at;
      else deferred.push({ kind: "notice", key: `n${event.seq}`, event });
      continue;
    }
    if (event.type === "op.requested") {
      if (decided.has(event.body.id) && !retainedRequests.has(event.body.id)) { work.push(event); continue; }
      const row = { kind: "request" as const, key: `r${event.seq}`, at: event.at, request: { ...event.body, state: "pending" as const } };
      requests.set(event.body.id, row);
      deferred.push(row);
      continue;
    }
    if (event.type === "session.restarted") {
      if (!event.body.imageUpdated) deferred.push({ kind: "restarted", key: `restart${event.seq}`, session: event.body.session, at: event.at });
      continue;
    }
    // Box lifecycle stays out of the feed; only a failed or lost box, or a close that failed to archive,
    // shows as a failed row with its cause in the owner's words.
    if (event.type === "box.changed") {
      const { to, cause } = event.body;
      // A lost box that then fails has said why already.
      if ((to === "failed" || to === "lost" || cause) && !(to === "failed" && event.body.from === "lost")) {
        deferred.push({ kind: "failed", key: `f${event.seq}`, seq: event.seq, session: event.body.session, at: event.at, ...boxRow(to, cause) });
      }
      continue;
    }
    if (event.type === "turn.ended") {
      // A final reply that no post of the session already carried is the session's answer here.
      const answer = event.body.final && (event.body.replies ?? []).find(({ reply, posted }) =>
        reply.kind === "final" && !posted && sameHome(reply.home ?? { kind: "feed" }, home));
      const turn = turns.get(event.body.turn);
      turns.delete(event.body.turn);
      if (event.body.final && answer) {
        post(`a${event.seq}`, event.seq, { kind: "session", session: event.body.session }, event.at, event.body.final, [], answer.reply.target ? [answer.reply.target] : []);
      }
      // A failed turn always gets its own row, even after its answer; it never folds into a work row. Its error is the
      // harness's raw text (r8), so the row shows only the owner words for its typed cause.
      if (event.body.outcome === "failed") {
        if (turn && turn.events.length > 1) work.push(...turn.events);
        const cause = event.body.cause;
        deferred.push({ kind: "failed", key: `f${event.seq}`, seq: event.seq, session: event.body.session, at: event.at, label: "Turn failed", cause: cause ? causes[cause] : "" });
      }
      const posted = turn?.posted || !!answer;
      const duration = turn ? Date.parse(event.at) - Date.parse(turn.at) : NaN;
      // A turn whose start is outside the window keeps its row.
      if (event.body.outcome !== "failed" && (!posted || turn?.significant || event.body.outcome === "stopped" || !(duration < 60_000))) {
        if (turn) work.push(...turn.events);
        work.push(event);
      }
      continue;
    }
    if (event.type !== "session.profile_changed" && event.type !== "transcript.changed") work.push(event);
  }
  for (const sent of pending) post(`n${sent.id}`, 0, { kind: "owner" }, sent.at, sent.text, sent.attachments, sent.to, sent);
  flush();
  return rows;
}

// The local clock range elides a repeated day period, but keeps it when the range crosses noon.
export function timeSpan(start: string, end: string): string {
  const formatter = new Intl.DateTimeFormat([], { hour: "numeric", minute: "2-digit" });
  const a = formatter.formatToParts(new Date(start)), b = formatter.formatToParts(new Date(end));
  const period = (parts: Intl.DateTimeFormatPart[]) => parts.find(part => part.type === "dayPeriod")?.value;
  const first = a.filter(part => period(a) && period(a) === period(b) ? part.type !== "dayPeriod" : true).map(part => part.value).join("").trim();
  return `${first} – ${b.map(part => part.value).join("").trim()}`;
}

// Every event type has a plural group label, including types that do not currently appear in a work block.
export const eventGroups: Record<Event["type"], string> = {
  "connect.connector": "Connect connector changes", "account.failed": "account failures", "account.quota_low": "account quota alerts",
  "post.created": "posts", "session.started": "sessions started", "session.closed": "sessions closed",
  "session.restarted": "boxes replaced", "session.moved": "sessions moved", "session.profile_changed": "profiles changed",
  "turn.started": "turns started", "turn.ended": "turns ended", "turn.activity": "turn updates", "transcript.changed": "transcript updates",
  "computer.changed": "computer changes", "box.changed": "box changes", "box.pressure": "memory warnings",
  "room.paused": "pauses", "room.resumed": "resumes", "room.limits_changed": "room limits changed", "room.deleting": "room deletions", "room.deleted": "room deletions",
  "op.requested": "requests", "op.decided": "requests decided", "op.ran": "requests run",
  "image.built": "images built", "image.failed": "image builds failed", "pr.opened": "pull requests opened",
  "shell.job.ended": "jobs finished", "grant.expired": "host grants expired",
  "host.disk_low": "disk warnings", "conversation.read": "read positions",
};

// A full moment for a title, such as "Oct 31, 2026, 4:48 AM".
export const fullTime = (value: string): string => new Date(value).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });

export const clock = (at: string): string => clockFormatter.format(new Date(at));

// The muted time on a row's name line: the clock today, else the date and the clock. Every row that scrolls in calls
// it, and toLocaleTimeString builds a new formatter on each call, so the two formatters are made once.
const clockFormatter = new Intl.DateTimeFormat([], { hour: "numeric", minute: "2-digit" });
const date = new Intl.DateTimeFormat([], { month: "short", day: "numeric" });
export function formatTime(value: string): string {
  const at = new Date(value);
  return at.toDateString() === new Date().toDateString() ? clockFormatter.format(at) : `${date.format(at)}, ${clockFormatter.format(at)}`;
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

// A sentence that continues after a middle dot loses its period.
export const clause = (text: string) => text.replace(/\.$/, "");

// Files the browser shows inline as a thumbnail; other types get a file card.
export const isImage = (file: Attachment) => /^image\/(png|jpeg|gif|webp)$/.test(file.media);
// An image shows as a thumbnail until it fails to load, such as on a device without the files scope; then it becomes a file card.
export const isThumb = (file: Attachment, failed: readonly string[]) => isImage(file) && !failed.includes(file.sha256);

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

// How long an image build took, such as "under a second", "12 s" or "3 min".
const buildTime = (seconds: number) => seconds < 1 ? "under a second" : seconds < 60 ? `${Math.round(seconds)} s` : `${Math.round(seconds / 60)} min`;

// A session the room no longer lists.
export const closedSession = "A closed session";
const list = (parts: string[]) => parts.length < 2 ? parts.join("") : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;

// Why the engine closed a session, after "closed". The engine closes a session whose box stayed stopped for 7 days.
const closeReasons: Record<SessionCloseReason, string> = { dormant: "after 7 quiet days", disk: "to free disk" };

// One short line per work row in the owner's terms, such as "lead-web worked for 6 min · 2 turns · 1 job" or
// "builder-feed started · 3 sessions worked for 12 min · 4 turns"; the opened row lists each event.
// The time runs from the first turn's start to the last turn's end in the row, in whole minutes as T3 Code's
// "Worked for 20s"; without both ends the line says "lead-web worked · 2 turns". A DM's saved steps give the start
// that a reloaded window lacks.
export function workSummary(events: Event[], name: Name, until?: string, from?: string): string {
  if (events.length === 1 && !until && events[0].type !== "turn.started" && events[0].type !== "turn.ended" && events[0].type !== "shell.job.ended" && events[0].type !== "computer.changed" && !(events[0].type === "session.closed" && events[0].body.reason)) return describe(events[0], name) || "Activity";
  const turns = new Set<string>();
  let stopped = 0, jobs = 0, images = 0, prs = 0, started = 0, closed = 0, memory = 0;
  const approvals = new Map<string, number>();
  let first = Infinity, last = -Infinity;
  for (const event of events) {
    switch (event.type) {
      case "turn.started": turns.add(event.body.session); first = Math.min(first, Date.parse(event.at)); break;
      case "turn.ended":
        turns.add(event.body.session);
        last = Math.max(last, Date.parse(event.at));
        if (event.body.outcome === "stopped") stopped++;
        break;
      case "session.started": started++; break;
      case "session.closed": if (!event.body.reason) closed++; break;
      case "shell.job.ended": jobs++; break;
      case "image.built": images++; break;
      case "pr.opened": prs++; break;
      case "box.pressure": memory++; break;
      case "op.decided": if (event.body.decision.approved) { const by = event.body.decision.by.session ? name(event.body.decision.by.session) : "you"; approvals.set(by, (approvals.get(by) ?? 0) + 1); } break;
    }
  }
  if (until) last = Math.max(last, Date.parse(until));
  if (from) first = Math.min(first, Date.parse(from));
  const minutes = Math.round((last - first) / 60_000);
  const took = minutes < 1 ? "under a minute" : minutes < 60 ? `${minutes} min` : `${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} min` : ""}`;
  const sessions = [...turns];
  const named = sessions.slice(0, 3).map(name);
  const names = named.length < 2 ? named[0] : named.length === 2 ? named.join(" and ") : `${named.slice(0, -1).join(", ")} and ${named.at(-1)}`;
  const workers = sessions.length > 3 ? `${named.join(", ")} and ${sessions.length - 3} more` : names;
  const parts = [
    sessions.length && `${workers} worked${sessions.length === 1 && last >= first ? ` for ${took}` : ""}`,
    stopped && `${count(stopped, "turn")} stopped`,
    jobs && count(jobs, "job"),
    images && `${count(images, "image")} built`,
    prs && `${count(prs, "pull request")} opened`,
    ...[...approvals].map(([by, amount]) => `${count(amount, "approval")} by ${by}`),
    started && `${count(started, "session")} started`,
    closed && `${closed} closed`,
    memory && count(memory, "memory warning"),
  ].filter((part): part is string => typeof part === "string").slice(0, 4);
  return parts.join(" · ") || events.map(event => describe(event, name)).find(Boolean) || "Activity";
}

// Whether a work row has nothing to open: one event, which its summary already says. A request opens to its details and
// a PR to its link.
export function workIsBare(events: Event[], name: Name, until?: string): boolean {
  const [only] = events;
  return events.length === 1 && only.type !== "op.requested" && only.type !== "pr.opened" && workSummary(events, name, until) === describe(only, name);
}

// One plain line for an event inside an opened work row.
export function describe(event: Event, name: Name): string {
  switch (event.type) {
    case "session.restarted": return event.body.imageUpdated ? `${name(event.body.session)}'s box image was updated.` : `${name(event.body.session)}'s box was replaced. Unpushed work in the old volume is gone.`;
    case "session.started": return `${name(event.body.session)} started`;
    case "session.closed": return `${name(event.body.session)} closed${event.body.reason ? ` ${closeReasons[event.body.reason]}` : ""}`;
    case "session.moved": return `${name(event.body.session)} moved under ${name(event.body.newParent)}`;
    case "computer.changed": return `${name(event.body.session)} ${event.body.to === "running" ? "opened" : "closed"} its computer`;
    case "box.changed": return ""; // feedRows keeps box changes out of work rows.
    case "turn.started": return "";
    case "turn.ended": return event.body.outcome === "stopped" ? `${name(event.body.session)}'s turn stopped` : "";
    // Live only: the room store keeps it out of every feed window.
    case "transcript.changed": case "connect.connector": case "account.failed": case "account.quota_low": case "host.disk_low": case "conversation.read": return "";
    case "turn.activity": return event.body.label;
    case "shell.job.ended": {
      const who = name(event.body.session), job = event.body.description ? `job "${event.body.description}"` : "job";
      if (event.body.end?.kind === "stopped") return `${who} stopped its ${job}`;
      return failed(event.body) ? `${who}'s ${job} failed: ${endCause(event.body, who)}` : `${who}'s ${job} finished`;
    }
    case "op.decided": return `${event.body.decision.by.session ? name(event.body.decision.by.session) : "You"} ${event.body.decision.approved ? "approved" : "denied"} a request`;
    case "op.requested": return `${event.body.by.kind === "owner" ? "You" : event.body.by.session ? name(event.body.by.session) : "Dyna"} ${event.body.by.kind === "owner" ? event.body.card.title.replace(/^asks\b/, "ask") : event.body.card.title}${event.body.card.target.name ? ` · ${event.body.card.target.name}` : ""}`;
    case "op.ran": return "";
    case "image.built": return `Image for ${event.body.repo} built in ${buildTime(event.body.seconds)}`;
    // The reason is raw build output (r8); the build log holds it.
    case "image.failed": return `Image for ${event.body.repo} failed`;
    case "pr.opened": return `${name(event.body.session)} opened pull request ${event.body.repo}#${event.body.number}`;
    case "grant.expired": return "A host execution grant expired";
    // The owner reads this feed; a room's root may also pause it.
    case "room.paused": case "room.resumed": return `${event.from.session ? name(event.from.session) : "You"} ${event.type === "room.paused" ? "paused" : "resumed"} the room`;
    case "room.deleting": return "You deleted the room";
    case "room.deleted": return "";
    // Only the owner sets room limits. Zero means no limit. GB and MB count 2^30 and 2^20 bytes, as the Home meters do;
    // a limit under 1 GB shows in MB, rounded up to at least 1, so a real limit never reads as zero.
    case "room.limits_changed": {
      const { cpus, memBytes } = event.body, gb = memBytes / 2 ** 30;
      if (!cpus && !memBytes) return "You removed the room limit";
      const cpu = cpus ? `${cpus} CPU${cpus === 1 ? "" : "s"}` : "no CPU limit";
      const mem = !memBytes ? "no memory limit" : gb < 1 ? `${Math.max(1, Math.round(memBytes / 2 ** 20))} MB` : `${Number.isInteger(gb) ? gb : gb.toFixed(1)} GB`;
      return `You set the room limit to ${cpu} and ${mem}`;
    }
    case "session.profile_changed": return ""; // feedRows shows no row for it.
    case "box.pressure": return event.body.kind === "max" ? "A session reached its memory limit" : "A session is short of memory";
    case "post.created": return event.body.text;
  }
}
