import { createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { applyStep } from "./workflow.ts";
import type { WorkflowStatusLine, WorkflowStatusResponse, WorkflowStep } from "./contract.gen.ts";

// How many status lines the page keeps, as the engine does.
const STATUS_LINES = 20;

// One run as its page shows it: workflow.status once, then live step rows and status lines. The journal is the record, so the
// same read serves a closed crew, and a stream that dropped events reads again. Rows that arrive during a read go on top of it.
export class RunState {
  private status: WorkflowStatusResponse | undefined;
  private steps: WorkflowStep[] = [];
  private lines: WorkflowStatusLine[] = [];
  private failed = false;
  private early: WorkflowStep[] | undefined;
  private earlyLines: WorkflowStatusLine[] = [];
  private reads = 0;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });
  private readonly session: string;
  constructor(session: string) { this.session = session; }

  get snapshot() { this.subscribe(); return { status: this.status, steps: this.steps, lines: this.lines, failed: this.failed }; }

  // A row changes the list. Without one, the caller lost track of the run, or it ended, so it reads again.
  apply = (step?: WorkflowStep): void => {
    if (!step) { void this.read(); return; }
    if (this.early) { this.early.push(step); return; }
    this.steps = applyStep(this.steps, step);
    this.changed();
  };

  // A new status line goes after the ones the page has, unless a read already holds it.
  say = (line: WorkflowStatusLine): void => {
    if (this.early) { this.earlyLines.push(line); return; }
    this.lines = withLine(this.lines, line);
    this.changed();
  };

  async read(): Promise<void> {
    const asked = ++this.reads;
    this.early ??= [];
    try {
      const status = await call("workflow.status", { session: this.session });
      if (asked !== this.reads) return;
      this.status = status;
      this.steps = this.early.reduce(applyStep, status.steps ?? []);
      this.lines = this.earlyLines.reduce(withLine, status.statusLines ?? []);
      this.failed = false;
    } catch {
      if (asked !== this.reads) return;
      this.steps = this.early.reduce(applyStep, this.steps);
      this.lines = this.earlyLines.reduce(withLine, this.lines);
      this.failed = true;
    }
    this.early = undefined;
    this.earlyLines = [];
    this.changed();
  }
}

// The live event and the stored line take their times a moment apart, so the same text within two seconds is the same line.
const withLine = (lines: WorkflowStatusLine[], line: WorkflowStatusLine): WorkflowStatusLine[] =>
  lines.some(item => item.text === line.text && Math.abs(Date.parse(item.at) - Date.parse(line.at)) < 2000) ? lines : [...lines, line].slice(-STATUS_LINES);
