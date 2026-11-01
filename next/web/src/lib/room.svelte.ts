import { SvelteMap, SvelteSet, createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import { EventWindow } from "./window.svelte.ts";
import { pendingReason, type PendingReason } from "./event.ts";
import { boxFailures, countOf, failedJobs, statusOf, turnFailures, type BoxFailure, type Counts, type Status } from "./status.ts";
import { prKey } from "./feed.ts";
import type { BoxState, Conversation, FailureCause, Decision, Event, Goal, OpRequested, OpResult, OpState, PrSummary, Question, SessionStarted } from "./contract.gen.ts";
import { failed } from "./jobs.ts";


// An operation of the room as the Requests panel shows it: its card, then its decision and run.
export type Request = OpRequested & { state: OpState; decision?: Decision; result?: OpResult };

// A read position's key: "" for the feed, the session id for its DM.
const readKey = (conversation: Conversation) => conversation.kind === "dm" ? conversation.session ?? "" : "";

// Sessions, boxes, turns and operations come from session.list and op.list once, then from
// live events, never from history. Each open conversation reads its events in its own window.
export class RoomStore {
  readonly sessions = new SvelteMap<string, SessionStarted & { startedAt: string; lastTurnAt?: string }>();
  readonly boxes = new SvelteMap<string, BoxState>();
  // The machine holding each session's live box. A session without one has no entry.
  readonly hosts = new SvelteMap<string, string>();
  readonly turns = new SvelteMap<string, string>();
  // Sessions the owner asked to stop until their turn ends, and those whose request did not reach the engine.
  readonly stopping = new SvelteSet<string>();
  readonly stopFailed = new SvelteSet<string>();
  // The running turn's latest activity label per session, such as "read spec.md".
  readonly activity = new SvelteMap<string, string>();
  // The running turn's labels so far, newest last, kept while the page is open: the engine keeps only the latest one,
  // so a page opened mid-turn starts from that. A DM shows them as the turn's steps.
  readonly steps = new SvelteMap<string, string[]>();
  // Sessions whose last turn failed, with that turn.ended's seq.
  readonly failed = new SvelteMap<string, number>();
  // Why each failed turn failed, when the engine said; a session row shows it after "Failed".
  readonly causes = new SvelteMap<string, FailureCause>();
  // Sessions whose box failed or was lost, with why.
  readonly boxFailures = new SvelteMap<string, BoxFailure>();
  // Why each pending box waits, when admission said.
  readonly pendingReasons = new SvelteMap<string, PendingReason>();
  // The owner's read position per conversation, by readKey; the newest seq wins.
  private readonly read = new SvelteMap<string, number>();
  // Sessions whose box runs a computer the owner can watch.
  readonly computers = new SvelteSet<string>();
  // Background shell jobs still running, as of each session's last turn end and the jobs that ended since.
  readonly shellJobs = new SvelteMap<string, number>();
  // Jobs that failed, as "session/job", so the dock can mark one while its Jobs tab is closed.
  readonly failedJobs = new SvelteSet<string>();
  // The session and end seq of each failed job, so a read past the end clears its mark.
  private readonly failedEnds = new Map<string, { session: string; seq: number }>();
  // "session/job" keys that About subtracts from its running count, because the engine's list can lag.
  readonly endedJobs = new SvelteSet<string>();
  // Sessions with a job to reach: their box keeps a finished job's output.
  readonly hadJobs = new SvelteSet<string>();
  // Each session's active goals, oldest last, as goal.list last gave them. Any goal event reads them again.
  readonly goals = new SvelteMap<string, Goal[]>();
  private goalRead = 0;
  // op.list gives the pending operations; decided ones stay for the time the room is open.
  readonly requests = new SvelteMap<string, Request>();
  // question.list gives the waiting ones and the latest answered ones; the stream keeps them current.
  readonly questions = new SvelteMap<string, Question>();
  // Tracked PRs by prKey: pr.list once, then pr.changed. A room opens without them if that read fails.
  readonly prs = new SvelteMap<string, PrSummary>();
  // Names outlive closed sessions, so old posts still show who wrote them.
  readonly names = new SvelteMap<string, string>();
  private readonly windows = new Set<EventWindow>();
  // The newest event applied; session.list gives the first value and the stream resumes after it.
  private head = 0;
  // Windows load after the lists, so their first page holds every event the stream skips.
  private opening: Promise<void> = Promise.resolve();
  private loaded = false;
  private failure: unknown;
  private stopStream = () => {};
  // Settles when the latest reread of read positions ends. Tests await it.
  rereading: Promise<void> = Promise.resolve();
  private closed = false;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });

  readonly room: string;
  private readonly connect: typeof stream;
  constructor(room: string, connect: typeof stream = stream) {
    this.room = room;
    this.connect = connect;
  }

  // A failure marks its session until the owner has read past it, in the feed or in the session's DM.
  unseen(id: string): boolean {
    const at = this.failed.get(id);
    return at !== undefined && Math.max(this.read.get("") ?? 0, this.read.get(id) ?? 0) < at;
  }
  // Keeps a read position: room.list gives the feed's, session.list each DM's, and conversation.read events move them.
  noteRead(conversation: Conversation, seq: number): void {
    if (seq <= (this.read.get(readKey(conversation)) ?? 0)) return;
    this.read.set(readKey(conversation), seq);
    this.clearReadJobs();
  }
  // A failed job counts until the owner reads past its end, in the feed or in its session's DM.
  private noteHost(session: string, box: BoxState, host?: string): void {
    if (host && box !== "deleted") this.hosts.set(session, host);
    else this.hosts.delete(session);
  }

  private failJob(session: string, job: string, seq: number): void {
    if (seq <= Math.max(this.readAt({ kind: "feed" }), this.readAt({ kind: "dm", session }))) return;
    this.failedEnds.set(`${session}/${job}`, { session, seq });
    this.failedJobs.add(`${session}/${job}`);
  }
  private clearReadJobs(): void {
    for (const [key, { session, seq }] of this.failedEnds) {
      if (seq > Math.max(this.readAt({ kind: "feed" }), this.readAt({ kind: "dm", session }))) continue;
      this.failedEnds.delete(key);
      this.failedJobs.delete(key);
    }
  }
  // The newest event the owner has read in a conversation, or 0.
  readAt(conversation: Conversation): number { return this.read.get(readKey(conversation)) ?? 0; }
  // The owner has seen a conversation up to seq; the engine keeps it for every device.
  see(conversation: Conversation, seq: number): void {
    if ((this.read.get(readKey(conversation)) ?? 0) >= seq) return;
    this.noteRead(conversation, seq);
    // A failed write leaves the mark clear on this page only; other devices keep it until the next read.
    void call("conversation.read", { room: this.room, conversation, seq }).catch(() => {});
  }

  // True once session.list and op.list have loaded.
  get ready(): boolean { this.subscribe(); return this.loaded; }
  get error(): unknown { this.subscribe(); return this.failure; }
  // Pending owner-class operations wait for the owner.
  get waiting(): number { return [...this.requests.values()].filter(request => request.state === "pending" && request.class === "owner").length + this.pendingQuestions().length; }
  // The questions the owner has not answered, of one session or the room, oldest first.
  pendingQuestions(session?: string): Question[] {
    return [...this.questions.values()].filter(question => question.state === "pending" && (!session || question.session === session))
      .sort((a, b) => a.asked.localeCompare(b.asked));
  }
  // The turn may end before the answer comes, so only a turn still running counts as stopping.
  stop(id: string): void {
    this.stopFailed.delete(id);
    void call("session.stop", { session: id }).then(({ stopping }) => { if (stopping && this.turns.has(id)) this.stopping.add(id); },
      () => { this.stopFailed.add(id); });
  }

  // Each session's one status (status.ts): a request it made or its box waits for the owner, its failure is unseen or
  // its box failed, or a turn runs. The header, the panel and the counts read only these.
  // A failed or lost box keeps its open turn for a restart to resume, but nothing is running it.
  running(id: string): boolean {
    const box = this.boxes.get(id);
    return this.turns.has(id) && box !== "failed" && box !== "lost";
  }
  status(id: string): Status { return this.statusWith(id, this.asking()); }
  statuses(): Map<string, Status> {
    const asking = this.asking();
    return new Map([...this.sessions.keys()].map(id => [id, this.statusWith(id, asking)]));
  }
  get counts(): Counts { return countOf(this.statuses().values()); }
  private statusWith(id: string, asking: Set<string>): Status {
    const box = this.boxes.get(id);
    // A box that no host takes for its room waits until the owner changes a host, so it waits for the owner too.
    const waiting = asking.has(id) || this.pendingReasons.get(id) === "room-host";
    return statusOf({ broken: box === "failed" || box === "lost", waiting, working: this.turns.has(id), failed: this.unseen(id) });
  }
  // The sessions whose requests or questions wait for the owner.
  private asking(): Set<string> {
    return new Set([...[...this.requests.values()].filter(request => request.state === "pending" && request.class === "owner")
      .flatMap(request => request.by.session ? [request.by.session] : []), ...this.pendingQuestions().map(question => question.session)]);
  }

  async open(): Promise<void> {
    this.opening = this.load();
    await this.opening.then(() => { this.loaded = true; }, (error: unknown) => { this.failure = error; });
    this.changed();
    if (this.closed || !this.loaded) return;
    void this.loadPRs();
    // A failed job counts until the owner reads past its end, as a failed turn does. The first page does not wait for this read.
    if (this.hadJobs.size) void failedJobs(this.room, this.head + 1).then(ends => {
      for (const { session, job, seq } of ends) this.failJob(session, job, seq);
    });
    this.stopStream = this.connect(this.room, () => this.head, event => this.apply(event), undefined, undefined, () => {
      // A conversation.read is not replayed. A read made after load() or during a dropped stream is missed, so each hello rereads.
      this.rereading = this.rereadPositions().catch(() => {});
    });
  }

  // A pr.changed that arrived first is newer than this read, so it stays.
  private async loadPRs(): Promise<void> {
    const listed = await call("pr.list", { room: this.room, all: true }).catch(() => undefined);
    for (const pr of listed?.prs ?? []) {
      if (!this.prs.has(prKey(pr.repo, pr.number))) this.prs.set(prKey(pr.repo, pr.number), pr);
    }
  }

  // room.list gives the feed's read position.
  private async readFeed(): Promise<void> {
    const rooms = await call("room.list", {});
    const own = (rooms.rooms ?? []).find(summary => summary.id === this.room);
    if (own) this.noteRead({ kind: "feed" }, own.lastRead);
  }

  // Rereads every read position: room.list for the feed, session.list for each DM.
  async rereadPositions(): Promise<void> {
    await this.readFeed();
    const list = await call("session.list", { room: this.room });
    for (const session of list.sessions ?? []) this.noteRead({ kind: "dm", session: session.id }, session.lastRead);
  }

  private async load(): Promise<void> {
    const list = await call("session.list", { room: this.room });
    // op.list reads after the seq, so an operation it misses arrives as a live event.
    const listed = await call("op.list", { room: this.room });
    const asked = await call("question.list", { room: this.room });
    // The feed position must be known before failed jobs are classified against it.
    await this.readFeed();
    this.head = list.seq;
    void this.readGoals();
    for (const session of list.sessions ?? []) {
      this.sessions.set(session.id, {
        session: session.id, name: session.name, parent: session.parent || undefined,
        profile: session.profile, goal: session.goal || undefined, startedAt: session.startedAt, lastTurnAt: session.lastTurnAt,
      });
      this.names.set(session.id, session.name);
      this.noteRead({ kind: "dm", session: session.id }, session.lastRead);
      this.boxes.set(session.id, session.boxState);
      this.noteHost(session.id, session.boxState, session.host);
      if (session.boxState === "failed" || session.boxState === "lost") this.boxFailures.set(session.id, { box: session.boxState });
      const reason = pendingReason(session.boxState, session.boxReason);
      if (reason) this.pendingReasons.set(session.id, reason);
      if (session.turn) this.turns.set(session.id, session.turn);
      if (session.turn && session.activity) { this.activity.set(session.id, session.activity); this.steps.set(session.id, [session.activity]); }
      if (session.lastOutcome === "failed") this.failed.set(session.id, list.seq);
      if (session.computer === "running") this.computers.add(session.id);
      this.shellJobs.set(session.id, session.shellJobs ?? 0);
      if (session.hadJobs || session.shellJobs) this.hadJobs.add(session.id);
    }
    if (this.boxFailures.size) for (const [id, failure] of await boxFailures(this.room, list.seq + 1)) if (this.boxFailures.has(id)) this.boxFailures.set(id, failure);
    // A failure turnFailures cannot place keeps list.seq and shows.
    for (const [session, { seq, body }] of await turnFailures(this.room, list.seq + 1, this.failed.keys())) {
      this.failed.set(session, seq);
      if (body.cause) this.causes.set(session, body.cause);
    }
    for (const { id, kind, by, class: level, card, expires, state } of listed.operations ?? []) {
      this.requests.set(id, { id, kind, by, class: level, card, expires, state });
    }
    for (const question of asked.questions ?? []) this.questions.set(question.id, question);
  }

  private async readGoals(): Promise<void> {
    const asked = ++this.goalRead;
    const list = await call("goal.list", { room: this.room }).catch(() => undefined);
    if (!list || asked !== this.goalRead) return;
    this.goals.clear();
    for (const goal of list.goals ?? []) if (goal.session) this.goals.set(goal.session, [...this.goals.get(goal.session) ?? [], goal]);
  }

  // session.list leaves out the open turn of a broken box, so a box that comes back reads it again.
  private async readTurns(): Promise<void> {
    const list = await call("session.list", { room: this.room }).catch(() => undefined);
    if (!list || list.seq < this.head) return;
    for (const session of list.sessions ?? []) if (session.turn) this.turns.set(session.id, session.turn);
  }

  close(): void { this.closed = true; this.stopStream(); }

  // Opens a window on one conversation; the view that opened it calls leave when it goes.
  window(home: Conversation): EventWindow {
    const opened = new EventWindow(this.room, home, names => {
      for (const [session, name] of Object.entries(names)) this.names.set(session, name);
    });
    this.windows.add(opened);
    void opened.open(this.opening);
    return opened;
  }

  leave(window: EventWindow): void { this.windows.delete(window); }

  apply(event: Event): void {
    // A read position has seq 0 and moves only forward.
    if (event.type === "conversation.read") { this.noteRead(event.body.conversation, event.body.seq); return; }
    // A live activity label has seq 0 and never reaches a window; a label of another turn is stale.
    if (event.type === "turn.activity") {
      if (this.turns.get(event.body.session) === event.body.turn) {
        this.activity.set(event.body.session, event.body.label);
        const steps = this.steps.get(event.body.session) ?? [];
        if (steps.at(-1) !== event.body.label) this.steps.set(event.body.session, [...steps, event.body.label].slice(-50));
      }
      return;
    }
    if (event.seq <= this.head) return;
    this.head = event.seq;
    this.step(event);
    for (const window of this.windows) window.apply(event);
  }

  private step(event: Event): void {
    const ended = event.type === "turn.ended" && this.sessions.get(event.body.session);
    if (event.type === "turn.ended" && ended) this.sessions.set(event.body.session, { ...ended, lastTurnAt: event.at });
    switch (event.type) {
      case "session.started":
        this.sessions.set(event.body.session, { ...event.body, startedAt: event.at });
        this.names.set(event.body.session, event.body.name);
        if (event.body.goal) void this.readGoals();
        break;
      case "session.closed":
        this.sessions.delete(event.body.session);
        this.activity.delete(event.body.session);
        this.steps.delete(event.body.session);
        void this.readGoals(); // Its goals moved to its parent.
        break;
      case "goal.set": case "goal.item_changed": case "goal.ended":
        void this.readGoals();
        break;
      case "session.profile_changed": {
        const changed = this.sessions.get(event.body.session);
        if (changed) this.sessions.set(event.body.session, { ...changed, profile: event.body.profile });
        break;
      }
      case "session.moved": {
        const moved = this.sessions.get(event.body.session);
        if (moved) this.sessions.set(event.body.session, { ...moved, parent: event.body.newParent });
        break;
      }
      case "box.changed": {
        const { session, to, cause } = event.body, reason = pendingReason(to, event.body.reason);
        this.boxes.set(session, to);
        // Placing or moving a box changes its host, and a deleted box keeps naming the one it left.
        this.noteHost(session, to, event.body.host);
        if (to === "failed" || to === "lost") this.boxFailures.set(session, { box: to, cause: cause ?? this.boxFailures.get(session)?.cause, at: event.at });
        else this.boxFailures.delete(session);
        // A pending to pending change can name a new reason.
        if (reason) this.pendingReasons.set(session, reason);
        else this.pendingReasons.delete(session);
        // A computer runs only inside a ready box.
        if (to !== "ready") this.computers.delete(session);
        if (to === "ready" && !this.turns.has(session)) void this.readTurns();
        break;
      }
      case "pr.changed":
        for (const { pr } of event.body.changes ?? []) this.prs.set(prKey(pr.repo, pr.number), pr);
        break;
      case "computer.changed":
        if (event.body.to === "running") this.computers.add(event.body.session);
        else this.computers.delete(event.body.session);
        break;
      case "turn.started":
        this.turns.set(event.body.session, event.body.turn);
        this.activity.delete(event.body.session);
        this.steps.delete(event.body.session);
        break;
      case "turn.ended":
        if (this.turns.get(event.body.session) === event.body.turn) {
          this.turns.delete(event.body.session);
          this.stopping.delete(event.body.session);
          this.stopFailed.delete(event.body.session);
          this.activity.delete(event.body.session);
          this.steps.delete(event.body.session);
        }
        // Only a completed turn clears the failure; a stopped turn did not succeed.
        if (event.body.outcome === "failed") {
          this.failed.set(event.body.session, event.seq);
          if (event.body.cause) this.causes.set(event.body.session, event.body.cause); else this.causes.delete(event.body.session);
        } else if (event.body.outcome === "completed") { this.failed.delete(event.body.session); this.causes.delete(event.body.session); }
        this.shellJobs.set(event.body.session, event.body.shellJobs ?? 0);
        if (event.body.shellJobs) this.hadJobs.add(event.body.session);
        break;
      case "shell.job.ended":
        // A running turn's end replaces the count, as in the engine.
        if (!this.turns.has(event.body.session)) this.shellJobs.set(event.body.session, Math.max(0, (this.shellJobs.get(event.body.session) ?? 0) - 1));
        this.endedJobs.add(`${event.body.session}/${event.body.job}`);
        this.hadJobs.add(event.body.session);
        if (failed(event.body)) this.failJob(event.body.session, event.body.job, event.seq);
        break;
      case "op.requested": this.requests.set(event.body.id, { ...event.body, state: "pending" }); break;
      case "question.asked": this.questions.set(event.body.id, { id: event.body.id, room: event.room ?? this.room, session: event.body.session, text: event.body.text, state: "pending", asked: event.at }); break;
      case "question.answered": this.questions.set(event.body.id, { id: event.body.id, room: event.room ?? this.room, session: event.body.session, text: event.body.question, state: "answered", answer: event.body.answer, asked: this.questions.get(event.body.id)?.asked ?? event.at, answered: event.at }); break;
      case "question.withdrawn": {
        const known = this.questions.get(event.body.id);
        if (known) this.questions.set(event.body.id, { ...known, state: "withdrawn" });
        break;
      }
      case "op.decided": case "op.ran": {
        const known = this.requests.get(event.body.id);
        if (known) this.requests.set(event.body.id, { ...known, ...event.body });
        break;
      }
    }
  }
}
