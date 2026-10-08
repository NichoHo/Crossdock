import { createHmac } from "node:crypto";
import { config, type Role } from "./env";

/** Headers that make the Go API trust a signed-in user (spec 13.1). */
export function identityHeaders(user: string, role: Role, at = new Date()): Record<string, string> {
  const c = config();
  const identity = Buffer.from(`${user}|${role}|${Math.floor(at.getTime() / 1000)}`).toString("base64url");
  const signature = createHmac("sha256", c.signingKey).update(identity).digest("hex");
  return {
    Authorization: `Bearer ${c.serviceToken}`,
    "X-Crossdock-Identity": identity,
    "X-Crossdock-Signature": signature,
  };
}

/** Server-side call with a raw text body (the remittance CSV). */
export async function upstreamText<T>(
  s: { user: string; role: Role }, path: string, body: string, contentType = "text/csv",
): Promise<T> {
  const res = await fetch(config().apiUrl + path, {
    method: "POST", cache: "no-store", body,
    headers: { "Content-Type": contentType, ...identityHeaders(s.user, s.role) },
  });
  const text = await res.text();
  const json = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(res.status, json?.title ?? "request failed", json?.detail ?? res.statusText);
  return json as T;
}

export class ApiError extends Error {
  constructor(public status: number, public title: string, public detail: string) {
    super(`${title}: ${detail}`);
  }
}

/** Server-side call to the Go API as the given user. */
export async function upstream<T>(
  s: { user: string; role: Role },
  method: string,
  path: string,
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<T> {
  const res = await fetch(config().apiUrl + path, {
    method,
    cache: "no-store",
    headers: { ...(body === undefined ? {} : { "Content-Type": "application/json" }), ...identityHeaders(s.user, s.role), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const json = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(res.status, json?.title ?? "request failed", json?.detail ?? res.statusText);
  return json as T;
}
