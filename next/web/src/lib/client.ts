import type { Error as ContractError, MethodMap } from "./contract.gen.ts";

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
const signedOut = () => new APIError({ code: "forbidden", message: "You are signed out." });

// Why the page cannot show rooms, from a call's typed error; other errors belong to the view that made the call.
export type Blocked = "signed-out" | "unreachable";
export const blockedBy = (error: unknown): Blocked | undefined => !(error instanceof APIError) ? undefined
  : error.detail.code === "forbidden" ? "signed-out" : error.detail.code === "unavailable" ? "unreachable" : undefined;
// The wait before the next automatic try while the engine is not reachable: 1 s, doubling to at most 30 s.
export const retryDelay = (attempt: number) => Math.min(30_000, 1000 * 2 ** attempt);

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
