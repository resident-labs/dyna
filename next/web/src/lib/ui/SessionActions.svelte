<script lang="ts">
  import { call } from "../client.ts";
  import { confirmClose, confirmRestart } from "../session-confirms.ts";
  import { descendants, sessionActions, type SessionAction } from "../session-tree.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import MenuList from "./MenuList.svelte";
  // What the owner can do to a session, from its row in the sessions view. Move opens a second step in the same list.
  // Restart and Close cost something, so the menu gives way to the shared confirm, which says what first, with focus on
  // Cancel, and returns focus to the row after. A move stays until the engine answers: a refusal shows above the entries,
  // and the rows update from the events.
  let { store, session, anchor, side, trigger, onclose, onclosed }: {
    store: RoomStore; session: string; anchor: DOMRect; side: "start" | "end"; trigger: HTMLElement;
    onclose: (refocus: boolean) => void; onclosed: (sessions: string[]) => void;
  } = $props();
  type Entry = { key: string; label: string };
  const labels: Record<SessionAction, string> = { move: "Move under…", stop: "Stop", restart: "Restart box…", close: "Close session…" };
  let step: "actions" | "move" = $state("actions");
  let busy = $state(false), failure = $state("");
  let root: HTMLDivElement;
  // A press on the More button toggles the menu itself; a press anywhere else closes it, the row that opened it included.
  let opener = $derived(side === "end" ? trigger : undefined);
  let open = $derived(store.sessions.get(session));
  let name = $derived(open?.name ?? session);
  let sessions = $derived([...store.sessions.values()]);
  let under = $derived(descendants(sessions, session));
  // A session may move under any session in its room except itself and its descendants. The engine checks the same rule.
  let view: { label: string; items: Entry[]; note?: string } = $derived.by(() => {
    if (step === "move") return { label: `Move ${name} under`, items: sessions.filter(item => item.session !== session && !under.includes(item.session)).map(item => ({ key: item.session, label: item.name })), note: failure };
    const actions = sessionActions(open?.parent, (store.running(session) || store.starting(session)) && !store.stopping.has(session), store.boxes.get(session));
    return { label: `Actions for ${name}`, items: actions.map(key => ({ key, label: labels[key] })) };
  });
  // The menu closes first, and its props go with it, so these read what they need now.
  async function ask(action: "close" | "restart") {
    const id = session, label = name, below = [...under], names = (other: string) => store.names.get(other), done = onclosed, row = trigger;
    onclose(false);
    if (action === "restart") { await confirmRestart(id, label, row); return; }
    if (await confirmClose(id, label, below, names, row)) done([id, ...below]);
  }
  async function move(parent: string) {
    busy = true;
    failure = "";
    await call("session.move", { session, newParent: parent }).then(() => onclose(true), () => { failure = "The session did not move. Try again."; });
    busy = false;
  }
  function choose(key: string) {
    if (busy) return;
    if (step === "actions") {
      if (key === "stop") { store.stop(session); onclose(true); }
      else if (key === "close" || key === "restart") void ask(key);
      else step = "move";
    } else if (key === open?.parent) onclose(true);
    else void move(key);
  }
</script>

<svelte:window onpointerdown={event => { if (event.target instanceof Node && !root.contains(event.target) && !opener?.contains(event.target)) onclose(false); }} />
<div bind:this={root}>{#key step}
  <MenuList label={view.label} items={view.items} keyOf={entry => entry.key} checked={step === "move" ? open?.parent : undefined} {anchor} {side} note={view.note} onchoose={choose} {onclose}>
    {#snippet row(entry)}<span>{entry.label}</span>{/snippet}
  </MenuList>
{/key}</div>
