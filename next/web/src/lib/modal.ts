// The logic of the one modal (ui/Modal.svelte) and its confirm, apart from Svelte so that node can test it.

type Focusable = { isConnected: boolean; focus: (options?: FocusOptions) => void };

// A press that began and ended on the dialog itself, that is on its backdrop, closes it. A drag that started in the panel,
// such as a text selection, and ended out there does not.
export const onBackdrop = (dialog: unknown, down: unknown, up: unknown): boolean => down === dialog && up === dialog;

// Focus goes back to whatever opened the modal. A menu item that closed with its menu is gone, so the caller's own
// opener, such as the menu's button, comes first when it is given.
export function returnFocus(...openers: (Focusable | null | undefined)[]): Focusable | undefined {
  const target = openers.find(opener => opener?.isConnected) ?? undefined;
  target?.focus({ preventScroll: true });
  return target;
}

export type ConfirmOptions = {
  title: string;
  // What it costs, in the text color, then a muted note.
  message?: string;
  note?: string;
  // The button that does it, as "Close session". A danger action is red; anything else is the accent.
  action: string;
  danger?: boolean;
  // The work: the confirm stays open, busy, until it settles, and a failure keeps it open with the reason.
  run?: () => Promise<unknown>;
  failed?: string | ((error: unknown) => string);
  // Where focus goes after, when the control that asked is gone by then.
  opener?: HTMLElement | null;
};
export type Asking = ConfirmOptions & { id: number; busy: boolean; problem: string };

// The one confirm on screen. Asking again replaces it, as only one can show; the one replaced answers false. While its
// work runs, Cancel, Escape and the backdrop do nothing, so a close cannot leave the work unreported.
export class Confirmations {
  current: Asking | undefined = undefined;
  #listeners = new Set<() => void>();
  #resolve: ((ok: boolean) => void) | undefined;
  #next = 1;
  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    listener();
    return () => { this.#listeners.delete(listener); };
  }
  #emit(): void { for (const listener of this.#listeners) listener(); }
  #finish(ok: boolean): void {
    const resolve = this.#resolve;
    this.#resolve = undefined;
    this.current = undefined;
    this.#emit();
    resolve?.(ok);
  }
  // True once the work, if any, has succeeded; false when the owner leaves it.
  ask(options: ConfirmOptions): Promise<boolean> {
    if (this.current) this.#finish(false);
    this.current = { ...options, id: this.#next++, busy: false, problem: "" };
    const answer = new Promise<boolean>(resolve => { this.#resolve = resolve; });
    this.#emit();
    return answer;
  }
  cancel(): boolean {
    if (!this.current || this.current.busy) return false;
    this.#finish(false);
    return true;
  }
  async accept(): Promise<void> {
    const asking = this.current;
    if (!asking || asking.busy) return;
    if (!asking.run) { this.#finish(true); return; }
    this.current = { ...asking, busy: true, problem: "" };
    this.#emit();
    try {
      await asking.run();
      if (this.current?.id === asking.id) this.#finish(true);
    } catch (error: unknown) {
      if (this.current?.id !== asking.id) return;
      const problem = typeof asking.failed === "function" ? asking.failed(error) : asking.failed ?? "That did not work. Try again.";
      this.current = { ...asking, busy: false, problem };
      this.#emit();
    }
  }
}

export const confirmations = new Confirmations();
// Asks the owner first, in the shared confirm that ConfirmHost shows.
export const confirm = (options: ConfirmOptions): Promise<boolean> => confirmations.ask(options);
