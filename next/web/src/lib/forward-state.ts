import type { Quote } from "./feed.ts";

export type ForwardState = { quote?: Quote; previous?: Quote; draft: string };

export function replaceForward(state: ForwardState, quote: Quote): ForwardState {
  return { quote, previous: state.quote, draft: state.draft };
}

export function undoForward(state: ForwardState): ForwardState {
  return { quote: state.previous, draft: state.draft };
}

// Pending and failed posts may move slightly, but never forward.
export function canForwardPost(pending: boolean, waiting: boolean, text: string, files: number): boolean {
  return !pending && !waiting && (!!text.trim() || files > 0);
}
