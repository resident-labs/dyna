import type { Error as ContractError, MethodMap } from "./contract.gen.ts";

export class APIError extends Error {
  readonly detail: ContractError;
  constructor(detail: ContractError) {
    super(detail.message);
    this.detail = detail;
    this.name = "APIError";
  }
}

export async function call<M extends keyof MethodMap>(
  method: M, request: MethodMap[M]["request"],
): Promise<MethodMap[M]["response"]> {
  const response = await fetch(`/v1/${method}`, {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(request),
  });
  if (!response.ok) throw new APIError(await response.json() as ContractError);
  return await response.json() as MethodMap[M]["response"];
}
