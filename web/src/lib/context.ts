import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { getSession, WAREHOUSE_COOKIE, type Session } from "./session";
import { upstream, ApiError } from "./upstream";
import type { Page, Warehouse } from "./types";

export type Ctx = { session: Session; warehouses: Warehouse[]; wid: string };

/** Signed-in session plus the selected warehouse. Redirects to /login when signed out. */
export async function requireCtx(opts: { minRole?: "supervisor" } = {}): Promise<Ctx> {
  const session = await getSession();
  if (!session) redirect("/login");
  if (opts.minRole === "supervisor" && session.role === "operator") redirect("/scan");
  const { items: warehouses } = await upstream<Page<Warehouse>>(session, "GET", "/v1/warehouses");
  const chosen = (await cookies()).get(WAREHOUSE_COOKIE)?.value;
  const wid = warehouses.find((w) => w.id === chosen)?.id ?? warehouses[0]?.id ?? "";
  return { session, warehouses, wid };
}

/** Runs an API read and turns a failure into a message for the page's error banner. */
export async function load<T>(fn: () => Promise<T>): Promise<{ data?: T; error?: string }> {
  try {
    return { data: await fn() };
  } catch (e) {
    if (e instanceof ApiError) return { error: `${e.title}: ${e.detail}` };
    return { error: "The warehouse API could not be reached." };
  }
}

export const wpath = (wid: string, suffix: string) => `/v1/warehouses/${encodeURIComponent(wid)}${suffix}`;
