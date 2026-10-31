import type { ComputerInput, PhoneKeys } from "./computerInput.ts";

// A real, visually hidden textarea opens the phone keyboard, as in v1. It keeps one sentinel character so that mobile
// keyboards can report Backspace even after the text is cleared. Typed, composed and pasted text goes to the keys.
const sentinel = "​";

// Some keyboards send a plain Event for input, so the InputEvent fields are read from the Event.
const inputType = (event: Event) => "inputType" in event && typeof event.inputType === "string" ? event.inputType : "";
const composingNow = (event: Event) => "isComposing" in event && event.isComposing === true;

export function softKeyboard(node: HTMLTextAreaElement, keys: PhoneKeys, send: (inputs: ComputerInput[]) => void): () => void {
  let composing = false;
  const reset = () => { node.value = sentinel; node.setSelectionRange(1, 1); };
  const typed = (value: string) => send(keys.text(value));
  const input = (event: Event) => {
    if (composing || composingNow(event)) return;
    const type = inputType(event);
    if (type.startsWith("delete")) send(keys.erase(type.endsWith("Forward")));
    else typed(node.value.startsWith(sentinel) ? node.value.slice(1) : node.value);
    reset();
  };
  const start = () => { composing = true; };
  const end = (event: CompositionEvent) => { composing = false; typed(event.data ?? node.value.replace(sentinel, "")); reset(); };
  const paste = (event: ClipboardEvent) => {
    event.preventDefault();
    typed(event.clipboardData?.getData("text/plain") ?? "");
    reset();
  };
  // A hardware key on the phone: Enter, Backspace and the arrows make no text, so they go as keys, once.
  const keydown = (event: KeyboardEvent) => {
    if (composing || event.isComposing || event.keyCode === 229) return;
    const inputs = keys.named(event.key);
    if (!inputs.length) return;
    event.preventDefault();
    send(inputs);
  };
  node.addEventListener("keydown", keydown);
  node.addEventListener("input", input);
  node.addEventListener("compositionstart", start);
  node.addEventListener("compositionend", end);
  node.addEventListener("paste", paste);
  reset();
  return () => {
    node.removeEventListener("keydown", keydown);
    node.removeEventListener("input", input);
    node.removeEventListener("compositionstart", start);
    node.removeEventListener("compositionend", end);
    node.removeEventListener("paste", paste);
  };
}
