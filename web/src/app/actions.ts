"use server";

import { cookies } from "next/headers";
import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { getSession, WAREHOUSE_COOKIE } from "@/lib/session";
import { upstream, upstreamText, ApiError } from "@/lib/upstream";
import { wpath } from "@/lib/context";

async function need() {
  const s = await getSession();
  if (!s) redirect("/login");
  return s;
}

async function wid(s: { user: string; role: "operator" | "supervisor" | "admin" }) {
  const v = (await cookies()).get(WAREHOUSE_COOKIE)?.value;
  if (v) return v;
  const { items } = await upstream<{ items: { id: string }[] }>(s, "GET", "/v1/warehouses");
  if (!items[0]) throw new Error("no warehouse exists yet");
  return items[0].id;
}

export type ActionState = { error?: string; ok?: string; secret?: string };

/** Wraps an action so a failed API call becomes a message, not a crash. */
async function run(fn: () => Promise<string>): Promise<ActionState> {
  try {
    return { ok: await fn() };
  } catch (e) {
    return { error: e instanceof ApiError ? `${e.title}: ${e.detail}` : (e as Error).message };
  }
}

export async function selectWarehouse(form: FormData) {
  await need();
  (await cookies()).set(WAREHOUSE_COOKIE, String(form.get("wid") ?? ""), { path: "/", sameSite: "lax", httpOnly: true });
  revalidatePath("/", "layout");
}

export async function assignTask(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const op = String(form.get("operator") ?? "").trim();
    await upstream(s, "POST", wpath(await wid(s), `/tasks/${encodeURIComponent(String(form.get("id")))}/assign`), { operator: op });
    revalidatePath("/console/tasks");
    return `Assigned to ${op}`;
  });
}

export async function sweepAllocations(_: ActionState): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const r = await upstream<{ changed: number }>(s, "POST", wpath(await wid(s), "/allocation-sweeps"));
    revalidatePath("/console/orders");
    return `${r.changed} order(s) changed state`;
  });
}

export async function releaseWave(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const ids = form.getAll("order").map(String);
    if (ids.length === 0) throw new Error("select at least one allocated order");
    const w = await upstream<{ id: string; route_distance_m: number; pick_tasks: number }>(
      s, "POST", wpath(await wid(s), "/waves"), { order_ids: ids });
    revalidatePath("/console/orders");
    return `Wave ${w.id}: ${w.pick_tasks} pick tasks, route ${w.route_distance_m.toFixed(0)} m`;
  });
}

export async function cancelOrder(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    await upstream(s, "POST", wpath(await wid(s), `/orders/${encodeURIComponent(String(form.get("id")))}/cancel`));
    revalidatePath("/console/orders");
    return "Order cancelled and reservations released";
  });
}

export async function markShort(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    await upstream(s, "POST", wpath(await wid(s), `/asn-lines/${encodeURIComponent(String(form.get("id")))}/short`));
    revalidatePath("/console/receiving");
    return "Line closed as short";
  });
}

/** Like run, but returns a one-time secret to show the user once. */
async function runWithSecret(fn: () => Promise<{ ok: string; secret: string }>): Promise<ActionState> {
  try {
    return await fn();
  } catch (e) {
    return { error: e instanceof ApiError ? `${e.title}: ${e.detail}` : (e as Error).message };
  }
}

export async function createEndpoint(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return runWithSecret(async () => {
    const events = String(form.get("events") ?? "").split(/[\s,]+/).filter(Boolean);
    const r = await upstream<{ endpoint: { id: string }; secret: string }>(s, "POST", wpath(await wid(s), "/endpoints"),
      { url: String(form.get("url") ?? "").trim(), events });
    revalidatePath("/console/endpoints");
    return { ok: `Endpoint ${r.endpoint.id} created.`, secret: r.secret };
  });
}

export async function rotateSecret(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return runWithSecret(async () => {
    const r = await upstream<{ secret: string; previous_valid_hours: number }>(
      s, "POST", wpath(await wid(s), `/endpoints/${encodeURIComponent(String(form.get("id")))}/rotate-secret`), {});
    return { ok: `New secret issued. The old one stays valid for ${r.previous_valid_hours} hours.`, secret: r.secret };
  });
}

export async function disableEndpoint(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    await upstream(s, "DELETE", wpath(await wid(s), `/endpoints/${encodeURIComponent(String(form.get("id")))}`));
    revalidatePath("/console/endpoints");
    return "Endpoint disabled. Pending deliveries are kept and can be replayed.";
  });
}

export async function replayDeliveries(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const id = encodeURIComponent(String(form.get("id")));
    const mode = String(form.get("mode"));
    const body: Record<string, unknown> =
      mode === "dead" ? { dead_letters: true }
      : mode === "event" ? { event_id: String(form.get("event_id") ?? "").trim() }
      : { from: new Date(String(form.get("from"))).toISOString(), to: new Date(String(form.get("to"))).toISOString() };
    const r = await upstream<{ queued: number }>(s, "POST", wpath(await wid(s), `/endpoints/${id}/replay`), body);
    revalidatePath(`/console/endpoints/${id}`);
    return `${r.queued} replay${r.queued === 1 ? "" : "s"} queued`;
  });
}

export async function scheduleCounts(_: ActionState): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const r = await upstream<{ created: number }>(s, "POST", wpath(await wid(s), "/count-schedules"), {});
    revalidatePath("/console/reconciliation");
    return `${r.created} count task${r.created === 1 ? "" : "s"} created`;
  });
}

export async function runReconciliation(_: ActionState): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const r = await upstream<{ opened: number; cleared: number }>(s, "POST", wpath(await wid(s), "/reconciliations"), {});
    revalidatePath("/console/reconciliation");
    return `${r.opened} opened, ${r.cleared} cleared`;
  });
}

export async function confirmVariance(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const id = encodeURIComponent(String(form.get("id")));
    await upstream(s, "POST", wpath(await wid(s), `/variances/${id}/confirm`), {
      classification: String(form.get("classification")), note: String(form.get("note") ?? ""),
    });
    revalidatePath("/console/reconciliation");
    return "Confirmed";
  });
}

export async function waiveBreak(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const id = encodeURIComponent(String(form.get("id")));
    await upstream(s, "POST", wpath(await wid(s), `/recon-breaks/${id}/waive`), { note: String(form.get("note") ?? "") });
    revalidatePath("/console/reconciliation");
    return "Waived";
  });
}

const minor = (v: FormDataEntryValue | null) => Math.round(Number(String(v ?? "0").replace(/,/g, "")) * 100);

export async function uploadRemittance(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const file = form.get("file");
    if (!(file instanceof File) || file.size === 0) throw new Error("choose a CSV file");
    const carrier = String(form.get("carrier") ?? "").trim();
    const q = new URLSearchParams({ carrier, filename: file.name });
    const r = await upstreamText<{ lines: number; matched: number; breaks: Record<string, number>; duplicate: boolean }>(
      s, wpath(await wid(s), `/remittances?${q}`), await file.text());
    revalidatePath("/console/settlement");
    if (r.duplicate) return "That exact file was uploaded before. Nothing changed.";
    const n = Object.values(r.breaks).reduce((a, b) => a + b, 0);
    return `${r.lines} lines: ${r.matched} matched, ${n} need attention`;
  });
}

export async function scanUnremitted(_: ActionState): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const r = await upstream<{ unremitted_opened: number }>(s, "POST", wpath(await wid(s), "/settlement-scans"), {});
    revalidatePath("/console/settlement");
    return `${r.unremitted_opened} unpaid shipment${r.unremitted_opened === 1 ? "" : "s"} found`;
  });
}

export async function resolveSettlementBreak(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const id = encodeURIComponent(String(form.get("id")));
    await upstream(s, "POST", wpath(await wid(s), `/settlement-breaks/${id}/resolve`), {
      resolution: String(form.get("resolution")), note: String(form.get("note") ?? ""),
      writeoff_reason: String(form.get("writeoff_reason") ?? ""),
    });
    revalidatePath("/console/settlement");
    return "Case resolved";
  });
}

export async function saveCarrierTerms(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const carrier = encodeURIComponent(String(form.get("carrier") ?? "").trim());
    if (!carrier) throw new Error("carrier is required");
    await upstream(s, "PUT", wpath(await wid(s), `/carriers/${carrier}/terms`), {
      cod_fee_bps: Math.round(Number(form.get("fee_pct") ?? 0) * 100), cod_fee_flat_minor: minor(form.get("fee_flat")),
      shipping_cost_minor: minor(form.get("shipping")), settlement_days: Number(form.get("days") ?? 7),
    });
    revalidatePath("/console/settlement");
    return "Terms saved";
  });
}

export async function replayProposal(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    await upstream(s, "POST", wpath(await wid(s), `/slotting/proposals/${encodeURIComponent(String(form.get("id")))}/replay`), {});
    revalidatePath("/console/slotting");
    return "Replayed with the pick router";
  });
}

export async function acceptProposal(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    const r = await upstream<{ relocate_tasks: number }>(s, "POST", wpath(await wid(s), `/slotting/proposals/${encodeURIComponent(String(form.get("id")))}/accept`), {});
    revalidatePath("/console/slotting");
    return `${r.relocate_tasks} relocate task${r.relocate_tasks === 1 ? "" : "s"} created`;
  });
}

export async function rejectProposal(_: ActionState, form: FormData): Promise<ActionState> {
  const s = await need();
  return run(async () => {
    await upstream(s, "POST", wpath(await wid(s), `/slotting/proposals/${encodeURIComponent(String(form.get("id")))}/reject`), { note: String(form.get("note") ?? "") });
    revalidatePath("/console/slotting");
    return "Proposal rejected";
  });
}
