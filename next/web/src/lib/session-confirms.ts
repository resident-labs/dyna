import { APIError, call } from "./client.ts";
import { confirm } from "./modal.ts";
import { closeNote, handNote } from "./session-tree.ts";

// The confirms that cost a session something, shared by About, the session menu and a failed turn's row, so each one
// asks in the same words.

// Closing ends the session and every session under it. The confirm reads the session's list first, so it names what the
// close will really hand over; without the list it says less.
export async function confirmClose(session: string, name: string, under: string[], names: (id: string) => string | undefined, opener?: HTMLElement | null): Promise<boolean> {
  const handed = await call("session.responsibilities", { session }).then(({ close }) => handNote(close.handed ?? [], names(close.to) ?? "the parent"), () => "");
  return confirm({ title: `Close ${name}?`, message: handed || undefined, note: closeNote(under.map(id => names(id) ?? id)), action: "Close session", opener,
    run: () => call("session.close", { session }), failed: "The session did not close. Try again." });
}

export const restartCost = "Restart replaces this box. Its old volume and any work that was not pushed are lost. The session, its grants and its history stay.";

// A restart the engine refuses as a conflict has no restart left to offer: refused hears of it, and the confirm closes.
export function confirmRestart(session: string, name: string, opener?: HTMLElement | null, refused?: () => void): Promise<boolean> {
  return confirm({ title: `Restart ${name}'s box?`, message: restartCost, action: "Restart box", opener, failed: "The restart did not start. Try again.",
    run: () => call("session.restart", { session }).catch((error: unknown) => {
      if (refused && error instanceof APIError && error.detail.code === "conflict") { refused(); return; }
      throw error;
    }) });
}
