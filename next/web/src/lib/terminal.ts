import type { TerminalResize } from "./contract.gen.ts";

// An owner terminal opens a shell in a session's box or on a host (5.15).
export type TerminalTarget = { box: string } | { host: string };
export type TerminalSocket = { send(text: string): void; resize(cols: number, rows: number): void; close(): void };
// The part of WebSocket the adapter uses, so a test can pass a fake.
export interface Connection {
  binaryType: BinaryType;
  onopen: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent) => void) | null;
  onclose: ((event: CloseEvent) => void) | null;
  send(data: string | Uint8Array): void;
  close(): void;
}

// One WebSocket per tab, with no reattach (M2). The first resize connects, because the engine opens the
// shell at that size. Binary frames carry the terminal's bytes both ways, a TerminalResize text frame sets
// the size, and the engine's last text frame is TerminalExit. A socket that closes without one, such as on
// an engine restart, exits with no code. Frames sent before the socket opens wait for it, in order, so keys
// typed as the pane appears reach the shell.
export function openTerminal(
  target: TerminalTarget, handlers: { output(bytes: Uint8Array): void; exit(code?: number): void },
  connect: (path: string) => Connection = path => new WebSocket(new URL(path, location.href.replace(/^http/, "ws"))),
): TerminalSocket {
  const waiting: (string | Uint8Array)[] = [];
  let socket: Connection | undefined;
  let live: Connection | undefined;
  let code: number | undefined;
  let closed = false;
  const put = (frame: string | Uint8Array) => { if (live) live.send(frame); else waiting.push(frame); };
  const start = (size: TerminalResize) => {
    const query = new URLSearchParams({ ...target, cols: String(size.cols), rows: String(size.rows) });
    const opening = connect(`/v1/terminal?${query}`);
    socket = opening;
    opening.binaryType = "arraybuffer";
    opening.onopen = () => { for (const frame of waiting) opening.send(frame); waiting.length = 0; live = opening; };
    opening.onmessage = ({ data }) => {
      if (data instanceof ArrayBuffer) handlers.output(new Uint8Array(data));
      else if (typeof data === "string") {
        const exit: unknown = JSON.parse(data);
        if (typeof exit === "object" && exit && "exitCode" in exit && typeof exit.exitCode === "number" && Number.isInteger(exit.exitCode)) code = exit.exitCode;
      }
    };
    opening.onclose = () => { if (!closed) { closed = true; handlers.exit(code); } };
  };
  return {
    send: text => put(new TextEncoder().encode(text)),
    resize: (cols, rows) => { if (socket) put(JSON.stringify({ cols, rows } satisfies TerminalResize)); else start({ cols, rows }); },
    // The owner closed the tab; the engine hangs up the shell, and nothing waits for its exit.
    close: () => { closed = true; socket?.close(); },
  };
}
