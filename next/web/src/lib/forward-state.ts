import type { Quote } from "./feed.ts";

export type ForwardState = { quote?: Quote; previous?: Quote };

export function sameForward(first: Quote | undefined, second: Quote): boolean {
  return first?.author === second.author && first.at === second.at;
}

export function replaceForward(state: ForwardState, quote: Quote): ForwardState {
  return sameForward(state.quote, quote) ? state : { quote, previous: state.quote };
}

export function undoForward(state: ForwardState): ForwardState {
  return { quote: state.previous };
}

// Pending and failed posts may move slightly, but never forward.
export function canForwardPost(pending: boolean, waiting: boolean, text: string, files: number): boolean {
  return !pending && !waiting && (!!text.trim() || files > 0);
}
