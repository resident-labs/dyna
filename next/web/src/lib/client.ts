import type { Attachment, Error as ContractError, MethodMap } from "./contract.gen.ts";

export class APIError extends Error {
  readonly detail: ContractError;
  constructor(detail: ContractError) {
    super(detail.message);
    this.detail = detail;
    this.name = "APIError";
  }
}

// Network failures and non-JSON answers (such as a proxy's HTML 502) become one typed error.
const unavailable = () => new APIError({ code: "unavailable", message: "The engine is not reachable." });

export async function call<M extends keyof MethodMap>(
  method: M, request: MethodMap[M]["request"],
): Promise<MethodMap[M]["response"]> {
  const response = await fetch(`/v1/${method}`, {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(request),
  }).catch(() => { throw unavailable(); });
  const body: unknown = await response.json().catch(() => { throw unavailable(); });
  // The engine answers with the contract's JSON: an Error when the status is not OK,
  // else the method's response. Callers that read events check them with decodeEvent.
  if (!response.ok) throw new APIError(body as ContractError);
  return body as MethodMap[M]["response"];
}

// Stores one file in the engine; the answer is the Attachment a post carries. The engine answers a
// failed upload with plain text, so any failure is the one typed error.
export async function upload(file: File): Promise<Attachment> {
  const response = await fetch(`/v1/files?name=${encodeURIComponent(file.name)}`, {
    method: "POST", credentials: "same-origin", headers: { "Content-Type": file.type || "application/octet-stream" }, body: file,
  }).catch(() => { throw unavailable(); });
  if (!response.ok) throw unavailable();
  // The engine's own JSON, as in call.
  return await response.json().catch(() => { throw unavailable(); }) as Attachment;
}
