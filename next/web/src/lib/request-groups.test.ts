import { test } from "node:test";
import { deepStrictEqual } from "node:assert";
import { requestGroups } from "./request-groups.ts";
import type { Request } from "./room.svelte.ts";

test("groups requests by session, largest first, retaining expiry order", () => {
  const item = (id: string, session: string): Request => ({
    id, kind: "test", by: { kind: "session", session }, class: "owner",
    card: { title: "asks to run", target: { kind: "room", name: "room" } },
    expires: "2026-01-01T00:00:00Z", state: "pending",
  });
  const a = item("a", "one"), b = item("b", "two"), c = item("c", "two");
  deepStrictEqual(requestGroups([a, b, c]), [["two", [b, c]], ["one", [a]]]);
});
