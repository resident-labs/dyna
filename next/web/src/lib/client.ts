import type { Attachment, Error as ContractError, MethodMap } from "./contract.gen.ts";

export class APIError extends Error {
  readonly detail: ContractError;
  constructor(detail: ContractError) {
    super(detail.message);
    this.detail = detail;
    this.name = "APIError";
  }
}

// Network failures and non-JSON answers (such as a proxy's HTML 502) become one typed error. The web server refuses a
// missing or ended sign-in with a plain-text 403 before the API sees the call, so that becomes forbidden.
const unavailable = () => new APIError({ code: "unavailable", message: "The engine is not reachable." });
export class SignedOut extends APIError {}
const signedOut = () => new SignedOut({ code: "forbidden", message: "You are signed out." });

// Why the page cannot show rooms, from a call's typed error; other errors belong to the view that made the call.
export type Blocked = "signed-out" | "unreachable";
export const blockedBy = (error: unknown): Blocked | undefined => !(error instanceof APIError) ? undefined
  : error.detail.code === "forbidden" ? "signed-out" : error.detail.code === "unavailable" ? "unreachable" : undefined;
// The wait before the next automatic try while the engine is not reachable: 1 s, doubling to at most 30 s.
export const retryDelay = (attempt: number) => Math.min(30_000, 1000 * 2 ** attempt);

// /v1/pair sets this browser's device cookie, so it answers by status: 403 for a wrong code, 410 with no live code.
export type PairResult = "paired" | "wrong" | "ended" | "unreachable";
export async function redeemPair(code: string, name: string): Promise<PairResult> {
  const response = await fetch("/v1/pair", {
    method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ code, name }),
  }).catch(() => undefined);
  return response?.ok ? "paired" : response?.status === 410 ? "ended" : response?.status === 403 ? "wrong" : "unreachable";
}

export async function call<M extends keyof MethodMap>(
  method: M, request: MethodMap[M]["request"],
): Promise<MethodMap[M]["response"]> {
  const response = await fetch(`/v1/${method}`, {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(request),
  }).catch(() => { throw unavailable(); });
  const body: unknown = await response.json().catch(() => { throw response.status === 403 ? signedOut() : unavailable(); });
  // The engine answers with the contract's JSON: an Error when the status is not OK,
  // else the method's response. Callers that read events check them with decodeEvent.
  if (!response.ok) throw new APIError(body as ContractError);
  return body as MethodMap[M]["response"];
}

// Stores one file in a room; the answer is the Attachment a post carries. The engine answers a
// failed upload with plain text, so any failure is the one typed error.
export async function upload(file: File, room: string): Promise<Attachment> {
  const response = await fetch(`/v1/files?name=${encodeURIComponent(file.name)}&room=${encodeURIComponent(room)}`, {
    method: "POST", credentials: "same-origin", headers: { "Content-Type": file.type || "application/octet-stream" }, body: file,
  }).catch(() => { throw unavailable(); });
  if (!response.ok) throw unavailable();
  // The engine's own JSON, as in call.
  return await response.json().catch(() => { throw unavailable(); }) as Attachment;
}
