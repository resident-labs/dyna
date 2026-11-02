import { createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { applyStep } from "./workflow.ts";
import type { WorkflowStatusResponse, WorkflowStep } from "./contract.gen.ts";

// One run as its page shows it: workflow.status once, then live step rows. The journal is the record, so the same read serves a
// closed crew, and a stream that dropped events reads again. Rows that arrive during a read go on top of it.
export class RunState {
  private status: WorkflowStatusResponse | undefined;
  private steps: WorkflowStep[] = [];
  private failed = false;
  private early: WorkflowStep[] | undefined;
  private reads = 0;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });
  private readonly session: string;
  constructor(session: string) { this.session = session; }

  get snapshot() { this.subscribe(); return { status: this.status, steps: this.steps, failed: this.failed }; }

  // A row changes the list. Without one, the caller lost track of the run, or it ended, so it reads again.
  apply = (step?: WorkflowStep): void => {
    if (!step) { void this.read(); return; }
    if (this.early) { this.early.push(step); return; }
    this.steps = applyStep(this.steps, step);
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
      this.failed = false;
    } catch {
      if (asked !== this.reads) return;
      this.steps = this.early.reduce(applyStep, this.steps);
      this.failed = true;
    }
    this.early = undefined;
    this.changed();
  }
}
