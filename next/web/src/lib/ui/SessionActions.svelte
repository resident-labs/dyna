<script lang="ts">
  import { call } from "../client.ts";
  import { closeNote, descendants, handNote, sessionActions, type SessionAction } from "../session-tree.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import MenuList from "./MenuList.svelte";
  // What the owner can do to a session, from its row in the sessions view. Move, Restart and Close each open a second
  // step in the same list. A step that costs something says so first, with focus on Cancel, so a stray Enter never runs
  // it. The menu stays until the engine answers: a refusal shows above the entries, and the rows update from the events.
  let { store, session, anchor, side, trigger, onclose, onclosed }: {
    store: RoomStore; session: string; anchor: DOMRect; side: "start" | "end"; trigger: HTMLElement;
    onclose: (refocus: boolean) => void; onclosed: (sessions: string[]) => void;
  } = $props();
  type Entry = { key: string; label: string; danger?: boolean };
  const labels: Record<SessionAction, string> = { move: "Move under…", stop: "Stop turn", restart: "Restart box…", close: "Close session…" };
  const restartNote = "Restart replaces this box. Its old volume and any work that was not pushed are lost. The session, its grants and its history stay.";
  let step: SessionAction | "actions" = $state("actions");
  let busy = $state(false), failure = $state(""), handed = $state("");
  let root: HTMLDivElement;
  // A press on the More button toggles the menu itself; a press anywhere else closes it, the row that opened it included.
  let opener = $derived(side === "end" ? trigger : undefined);
  let open = $derived(store.sessions.get(session));
  let name = $derived(open?.name ?? session);
  let sessions = $derived([...store.sessions.values()]);
  let under = $derived(descendants(sessions, session));
  const confirm = (label: string): Entry[] => [{ key: "cancel", label: "Cancel" }, { key: "confirm", label, danger: true }];
  // A session may move under any session in its room except itself and its descendants. The engine checks the same rule.
  let view: { label: string; items: Entry[]; note?: string } = $derived.by(() => {
    if (step === "move") return { label: `Move ${name} under`, items: sessions.filter(item => item.session !== session && !under.includes(item.session)).map(item => ({ key: item.session, label: item.name })), note: failure };
    if (step === "close") return { label: `Close ${name}`, items: confirm("Close session"), note: failure || [handed, closeNote(under.map(id => store.names.get(id) ?? id))].filter(Boolean).join(" ") };
    if (step === "restart") return { label: `Restart ${name}'s box`, items: confirm("Restart box"), note: failure || restartNote };
    const actions = sessionActions(open?.parent, store.running(session) && !store.stopping.has(session), store.boxes.get(session));
    return { label: `Actions for ${name}`, items: actions.map(key => ({ key, label: labels[key] })) };
  });
  async function run(work: Promise<unknown>, refused: string, done: () => void) {
    busy = true;
    failure = "";
    await work.then(done, () => { failure = refused; });
    busy = false;
  }
  function choose(key: string) {
    if (busy) return;
    if (step === "actions") {
      if (key === "stop") { store.stop(session); onclose(true); } else step = key as SessionAction;
      // The second step reads the list now, so it names what the close will really hand over. Without it the step says less.
      if (step === "close") void call("session.responsibilities", { session }).then(({ close }) => { handed = handNote(close.handed ?? [], store.names.get(close.to) ?? "the parent"); }, () => {});
    } else if (key === "cancel" || key === open?.parent) onclose(true);
    else if (step === "move") void run(call("session.move", { session, newParent: key }), "The session did not move. Try again.", () => onclose(true));
    else if (step === "close") void run(call("session.close", { session }), "The session did not close. Try again.", () => onclosed([session, ...under]));
    else void run(call("session.restart", { session }), "The restart did not start. Try again.", () => onclose(true));
  }
</script>

<svelte:window onpointerdown={event => { if (event.target instanceof Node && !root.contains(event.target) && !opener?.contains(event.target)) onclose(false); }} />
<div bind:this={root}>{#key step}
  <MenuList label={view.label} items={view.items} keyOf={entry => entry.key} checked={step === "move" ? open?.parent : undefined} {anchor} {side} note={view.note} onchoose={choose} {onclose}>
    {#snippet row(entry)}<span class:danger={entry.danger}>{entry.label}</span>{/snippet}
  </MenuList>
{/key}</div>

<style>
  .danger { color: var(--danger); }
</style>
