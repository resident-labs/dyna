import type { Request } from "./room.svelte.ts";

// Keep the most urgent request first within each session, and the largest sessions first.
export function requestGroups(waiting: Request[]): [string, Request[]][] {
  const groups = new Map<string, Request[]>();
  for (const item of waiting) {
    const id = item.by.session ?? "";
    groups.set(id, [...groups.get(id) ?? [], item]);
  }
  return [...groups].sort((a, b) => b[1].length - a[1].length);
}

// Keyboard navigation follows the grouped screen order, then the shared requests.
export function requestOrder(waiting: Request[], shared: Request[], groups: [string, Request[]][], grouped: boolean): Request[] {
  return [...(grouped ? groups.flatMap(([, items]) => items) : waiting), ...shared];
}

// Scope the request list to the selected conversation.
export function requestsFor(requests: Iterable<Request>, session?: string): Request[] {
  return [...requests].filter(request => !session || request.by.session === session);
}
