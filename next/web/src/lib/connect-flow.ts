import { allowedOrigin, deviceKey, getPin, hello, proof, putPin, random, thumbprint, verifyHello, type Hello, type Pin } from "./connect.ts";

const stateKey = "dyna-connect-state";
const returnKey = "dyna-connect-return";
export type ConnectProblem = "state" | "code" | "limit" | "unreachable" | "relay";
export type Landing = { kind: "ok" } | { kind: "stop"; pin: Pin; offered: string; field: "key" | "relay" | "proof"; returning?: boolean }
  | { kind: "failed"; problem: ConnectProblem };

// A pinned key authenticates hello, and its separately pinned relay must also match.
export async function checkPin(greeting: Hello, pin: Pin | undefined): Promise<Landing | undefined> {
  if (!await verifyHello(greeting, greeting.key)) return pin ? { kind: "stop", pin, offered: "", field: "proof" } : { kind: "failed", problem: "unreachable" };
  if (!pin) return;
  if (greeting.key !== pin.key) return { kind: "stop", pin, offered: greeting.key, field: "key" };
  if (greeting.relay !== pin.relay) return { kind: "stop", pin, offered: greeting.relay, field: "relay" };
}
export async function beginConnect(greeting: Hello): Promise<Landing | undefined> {
  if (!allowedOrigin(greeting.relay)) return { kind: "failed", problem: "relay" };
  const state = random();
  sessionStorage.setItem(stateKey, state);
  sessionStorage.setItem("dyna-connect-machine", greeting.machine);
  sessionStorage.setItem("dyna-connect-key", greeting.key);
  sessionStorage.setItem("dyna-connect-relay", greeting.relay);
  sessionStorage.setItem(returnKey, location.pathname === "/connect" ? sessionStorage.getItem(returnKey) ?? "/" : location.pathname + location.search);
  const params = new URLSearchParams({ machine: greeting.machine, origin: location.origin, jkt: await thumbprint(await deviceKey()), state });
  location.assign(greeting.relay + "/open?" + params);
}
export async function finishConnect(fragment: string): Promise<Landing> {
  const params = new URLSearchParams(fragment.replace(/^#/, ""));
  // The one-use secret must leave browser history before any asynchronous work.
  history.replaceState(null, "", "/connect");
  const code = params.get("code"), handoff = params.get("handoff");
  const machine = params.get("mid"), key = params.get("mk");
  const relay = handoff ? params.get("relay") : sessionStorage.getItem("dyna-connect-relay");
  const expectedMachine = sessionStorage.getItem("dyna-connect-machine"), expectedKey = sessionStorage.getItem("dyna-connect-key");
  sessionStorage.removeItem("dyna-connect-machine"); sessionStorage.removeItem("dyna-connect-key"); sessionStorage.removeItem("dyna-connect-relay");
  if ((!code && !handoff) || !machine || !key || !relay || (code && (!sessionStorage.getItem(stateKey) || params.get("state") !== sessionStorage.getItem(stateKey) || machine !== expectedMachine || key !== expectedKey))) {
    return { kind: "failed", problem: "state" };
  }
  sessionStorage.removeItem(stateKey);
  const pin = await getPin(machine);
  if (pin && pin.key !== key) return { kind: "stop", pin, offered: key, field: "key", returning: true };
  let greeting: Hello | null;
  try { greeting = await hello(location.origin); } catch { return { kind: "failed", problem: "unreachable" }; }
  if (!greeting || greeting.machine !== machine) return { kind: "failed", problem: "state" };
  const stopped = await checkPin(greeting, pin);
  if (stopped) return stopped;
  if (greeting.relay !== relay) return { kind: "stop", pin: pin ?? { machine, key, relay, at: Date.now() }, offered: greeting.relay, field: "relay", returning: true };
  if (!allowedOrigin(greeting.relay)) return { kind: "failed", problem: "relay" };
  if (greeting.key !== key) {
    return pin ? { kind: "stop", pin, offered: greeting.key, field: "key", returning: true } : { kind: "failed", problem: "state" };
  }
  await putPin(pin ?? { machine, key, relay: greeting.relay, at: Date.now() });
  const response = await fetch("/api/connect/session", {
    method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Dyna": "1" },
    body: JSON.stringify({ ...(handoff ? { handoff } : { code }), proof: await proof(await deviceKey(), "/api/connect/session") }),
  }).catch(() => undefined);
  if (!response) return { kind: "failed", problem: "unreachable" };
  if (!response.ok) return { kind: "failed", problem: response.status === 429 ? "limit" : response.status === 403 ? "code" : "relay" };
  const route = params.get("route");
  if (handoff && route) sessionStorage.setItem("dyna-connect-move", new URLSearchParams({ route }).toString());
  const destination = (handoff ? params.get("return") : sessionStorage.getItem(returnKey)) ?? "/";
  sessionStorage.removeItem(returnKey);
  location.replace(destination.startsWith("/") && !destination.startsWith("//") ? destination : "/");
  return { kind: "ok" };
}
