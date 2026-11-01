import { test } from "node:test";
import assert from "node:assert/strict";
import { openTerminal, type Connection } from "./terminal.ts";

function fake() {
  const sent: (string | Uint8Array)[] = [];
  const socket: Connection = { binaryType: "blob", onopen: null, onmessage: null, onclose: null, send: data => { sent.push(data); }, close() {} };
  const message = (data: unknown) => socket.onmessage?.(new MessageEvent("message", { data }));
  // The adapter ignores close details; Node does not provide the browser's CloseEvent constructor.
  const close = () => socket.onclose?.(new Event("close") as CloseEvent);
  return { socket, sent, message, open: () => socket.onopen?.(new Event("open")), close };
}

test("the terminal adapter carries bytes, resizes and the shell's exit over one socket", () => {
  const socket = fake();
  const output: string[] = [];
  const exits: (number | undefined)[] = [];
  let path = "";
  const term = openTerminal({ box: "s1" }, { output: bytes => output.push(new TextDecoder().decode(bytes)), exit: code => exits.push(code) },
    url => { path = url; return socket.socket; });
  // Keys typed before the first size has opened the socket wait with the frames that follow, in order.
  term.send("é");
  term.resize(80, 24);
  assert.equal(path, "/v1/terminal?box=s1&cols=80&rows=24");
  assert.equal(socket.socket.binaryType, "arraybuffer");
  term.send("a");
  term.resize(100, 30);
  assert.equal(socket.sent.length, 0);
  socket.open();
  const key = (text: string) => new TextEncoder().encode(text);
  assert.deepEqual(socket.sent, [key("é"), key("a"), '{"cols":100,"rows":30}']);
  socket.message(new TextEncoder().encode("hi\r\n").buffer);
  assert.deepEqual(output, ["hi\r\n"]);
  // TerminalExit, then the engine's close, ends the tab with that code.
  socket.message('{"exitCode":2}');
  socket.close();
  assert.deepEqual([...exits], [2]);
  // A socket that closes with no TerminalExit, such as on an engine restart, exits with no code.
  const lost = fake();
  openTerminal({ host: "control-plane" }, { output: () => {}, exit: code => exits.push(code) }, () => lost.socket).resize(80, 24);
  lost.close();
  assert.deepEqual(exits, [2, undefined]);
});
