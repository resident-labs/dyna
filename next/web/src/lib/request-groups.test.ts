import { test } from "node:test";
import { deepStrictEqual } from "node:assert";
import { requestGroups, requestOrder, requestsFor } from "./request-groups.ts";
import type { Request } from "./room.svelte.ts";

test("groups requests by session, largest first, retaining expiry order", () => {
  const item = (id: string, session: string): Request => ({
    id, kind: "test", by: { kind: "session", session }, class: "owner",
    card: { title: "asks to run", target: { kind: "room", name: "room" } },
    expires: "2026-01-01T00:00:00Z", state: "pending",
  });
  const a = item("a", "one"), b = item("b", "two"), c = item("c", "two");
  const groups = requestGroups([a, b, c]);
  deepStrictEqual(groups, [["two", [b, c]], ["one", [a]]]);
  deepStrictEqual(requestOrder([a, b, c], [item("d", "three")], groups, true).map(request => request.id), ["b", "c", "a", "d"]);
  deepStrictEqual(requestOrder([a, b, c], [], groups, false).map(request => request.id), ["a", "b", "c"]);
});

test("scopes requests to the selected session", () => {
  const item = (id: string, session: string): Request => ({
    id, kind: "test", by: { kind: "session", session }, class: "owner",
    card: { title: "asks to run", target: { kind: "room", name: "room" } },
    expires: "2026-01-01T00:00:00Z", state: "pending",
  });
  const one = item("a", "one"), two = item("b", "two");
  deepStrictEqual(requestsFor([one, two]), [one, two]);
  deepStrictEqual(requestsFor([one, two], "one"), [one]);
});
