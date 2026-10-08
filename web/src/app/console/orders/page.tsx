import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { Order, Page } from "@/lib/types";
import { cancelOrder, releaseWave, sweepAllocations } from "../../actions";

export const metadata: Metadata = { title: "Orders and waves" };

const FILTERS = ["", "new", "partially_allocated", "unallocatable", "allocated", "waved", "packed", "shipped", "cancelled"];

export default async function OrdersPage({ searchParams }: { searchParams: Promise<{ state?: string; cursor?: string }> }) {
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const qs = new URLSearchParams({ limit: "50" });
  if (q.state) qs.set("state", q.state);
  if (q.cursor) qs.set("cursor", q.cursor);
  const { data, error } = await load(() => upstream<Page<Order>>(session, "GET", wpath(wid, `/orders?${qs}`)));

  return (
    <>
      <h1>Orders and waves</h1>
      <div className="row" style={{ marginBottom: 12 }}>
        <ActionForm action={sweepAllocations}>
          <SubmitButton className="btn secondary">Run allocation sweep</SubmitButton>
        </ActionForm>
      </div>
      <nav className="row" aria-label="Filter by state" style={{ marginBottom: 12 }}>
        {FILTERS.map((f) => (
          <Link key={f} className={`btn small ${q.state === f || (!q.state && f === "") ? "" : "secondary"}`}
            href={f ? `/console/orders?state=${f}` : "/console/orders"}>
            {f ? f.replaceAll("_", " ") : "all"}
          </Link>
        ))}
      </nav>
      {error && <div className="banner-error" role="alert">{error}</div>}
      {data && data.items.length === 0 && <div className="empty">No orders in this state.</div>}
      {data && data.items.length > 0 && (
        <ActionForm action={releaseWave}>
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th><span className="sr-only">Select for wave</span></th><th>Order</th><th>Reference</th><th>State</th><th>Wave</th><th></th></tr>
              </thead>
              <tbody>
                {data.items.map((o) => (
                  <tr key={o.id}>
                    <td>
                      {(o.state === "allocated" || o.state === "partially_allocated") && (
                        <input type="checkbox" name="order" value={o.id} aria-label={`Select ${o.ref}`} style={{ width: 24, height: 24 }} />
                      )}
                    </td>
                    <td className="code">{o.id}</td>
                    <td className="code">{o.ref}</td>
                    <td><StatusChip state={o.state} /></td>
                    <td className="code">{o.wave_id || "-"}</td>
                    <td>
                      <Link href={`/console/timeline?order_id=${encodeURIComponent(o.id)}`}>Timeline</Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p><SubmitButton>Release wave for selected orders</SubmitButton></p>
        </ActionForm>
      )}
      {data?.next_cursor && (
        <p><Link className="btn secondary" href={`/console/orders?${new URLSearchParams({ ...(q.state ? { state: q.state } : {}), cursor: data.next_cursor })}`}>Next page</Link></p>
      )}
      {data && data.items.some((o) => ["new", "allocated", "partially_allocated", "unallocatable", "waved"].includes(o.state)) && (
        <details className="panel" style={{ marginTop: 16 }}>
          <summary>Cancel an order</summary>
          <p className="muted">Releases every reservation once. Refused after stock is picked.</p>
          <ActionForm action={cancelOrder}>
            <div className="field">
              <label className="label" htmlFor="cancel-id">Order id</label>
              <input id="cancel-id" name="id" className="code" required placeholder="ord-..." />
            </div>
            <SubmitButton className="btn danger">Cancel order</SubmitButton>
          </ActionForm>
        </details>
      )}
    </>
  );
}
