import type { Metadata } from "next";
import Link from "next/link";
import { ClockCounterClockwise, Play, Prohibit } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
import { Tabs } from "@/components/Tabs";
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

  const nextHref = data?.next_cursor
    ? `/console/orders?${new URLSearchParams({ ...(q.state ? { state: q.state } : {}), cursor: data.next_cursor })}`
    : undefined;

  return (
    <>
      <PageHeader
        section="orders"
        title="Orders and waves"
        actions={
          <ActionForm action={sweepAllocations}>
            <SubmitButton className="btn secondary small"><Play size={14} aria-hidden="true" />Run allocation sweep</SubmitButton>
          </ActionForm>
        }
      />
      <div className="page-body">
        <div className="toolbar">
          <Tabs
            label="Filter by state"
            items={FILTERS.map((f) => ({
              href: f ? `/console/orders?state=${f}` : "/console/orders",
              label: f ? f.replaceAll("_", " ") : "all",
              active: q.state === f || (!q.state && f === ""),
            }))}
          />
        </div>
        {error && <Banner>{error}</Banner>}
        {data && data.items.length === 0 && <EmptyState>No orders in this state.</EmptyState>}
        {data && data.items.length > 0 && (
          <ActionForm action={releaseWave}>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <Th type="action"><span className="sr-only">Select for wave</span></Th>
                    <Th type="code">Order</Th><Th type="code">Reference</Th><Th type="status">State</Th><Th type="code">Wave</Th><Th type="action" />
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((o) => (
                    <tr key={o.id}>
                      <td className="check">
                        {(o.state === "allocated" || o.state === "partially_allocated") && (
                          <input type="checkbox" name="order" value={o.id} aria-label={`Select ${o.ref}`} />
                        )}
                      </td>
                      <td className="code">{o.id}</td>
                      <td className="code">{o.ref}</td>
                      <td><StatusChip state={o.state} /></td>
                      <td className="code">{o.wave_id || "-"}</td>
                      <td>
                        <Link className="btn ghost small" href={`/console/timeline?order_id=${encodeURIComponent(o.id)}`}>
                          <ClockCounterClockwise size={14} aria-hidden="true" />Timeline
                        </Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <TableFoot count={data.items.length} nextHref={nextHref}>
                <SubmitButton>Release wave for selected orders</SubmitButton>
              </TableFoot>
            </div>
          </ActionForm>
        )}
        {data && data.items.some((o) => ["new", "allocated", "partially_allocated", "unallocatable", "waved"].includes(o.state)) && (
          <details className="panel narrow">
            <summary>Cancel an order</summary>
            <p className="muted">Releases every reservation once. Refused after stock is picked.</p>
            <ActionForm action={cancelOrder}>
              <div className="field">
                <label className="label" htmlFor="cancel-id">Order id</label>
                <input id="cancel-id" name="id" className="code" required placeholder="ord-..." />
              </div>
              <SubmitButton className="btn danger"><Prohibit size={16} aria-hidden="true" />Cancel order</SubmitButton>
            </ActionForm>
          </details>
        )}
      </div>
    </>
  );
}
