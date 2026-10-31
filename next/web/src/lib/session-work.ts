import { call } from "./client.ts";
import { decodeEvent } from "./event.ts";
import type { Event } from "./contract.gen.ts";

type Start = Extract<Event, { type: "turn.started" }>;
type End = Extract<Event, { type: "turn.ended" }>;
type Post = Extract<Event, { type: "post.created" }>;
export type WorkTurn = { started: Start; ended?: End; inputs: Event[]; posts: Post[] };

export async function readWork(session: string, before?: string): Promise<{ turns: WorkTurn[]; more: boolean; total: number }> {
  const page = await call("session.work", { session, before });
  const turns = (page.turns ?? []).flatMap(turn => {
    const started = decodeEvent(turn.started);
    const ended = turn.ended && decodeEvent(turn.ended);
    if (!started || started.type !== "turn.started" || (ended && ended.type !== "turn.ended")) return [];
    const inputs = (turn.inputs ?? []).flatMap(event => { const decoded = decodeEvent(event); return decoded ? [decoded] : []; });
    const posts = (turn.posts ?? []).flatMap(event => { const decoded = decodeEvent(event); return decoded?.type === "post.created" ? [decoded] : []; });
    return [{ started, ended: ended || undefined, inputs, posts }];
  });
  return { turns, more: page.more, total: page.total };
}
