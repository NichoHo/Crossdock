import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
import { Tabs, stateTabs } from "@/components/Tabs";
import type { Page, ReturnAuth } from "@/lib/types";

export const metadata: Metadata = { title: "Returns" };

const STATES = ["", "authorised", "received", "dispositioned"];

export default async function ReturnsPage({ searchParams }: { searchParams: Promise<{ state?: string; cursor?: string }> }) {
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const qs = new URLSearchParams({ limit: "50" });
  if (q.state) qs.set("state", q.state);
  if (q.cursor) qs.set("cursor", q.cursor);
  const { data, error } = await load(() => upstream<Page<ReturnAuth>>(session, "GET", wpath(wid, `/returns?${qs}`)));

  return (
    <>
      <PageHeader section="returns" title="Returns" />
      <div className="page-body">
        <div className="toolbar">
          <Tabs label="Filter by state" items={stateTabs("/console/returns", STATES, q.state)} />
        </div>
        {error && <Banner>{error}</Banner>}
        {data && data.items.length === 0 && <EmptyState>No returns. A rejected cash-on-delivery parcel opens one automatically.</EmptyState>}
        {data && data.items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr><Th type="code">Return</Th><Th type="code">Tracking</Th><Th type="text">Reason</Th><Th type="status">State</Th><Th type="code">Units</Th><Th type="text">Decision</Th></tr>
              </thead>
              <tbody>
                {data.items.map((r) => (
                  <tr key={r.id}>
                    <td className="code">{r.id}</td>
                    <td className="code"><Link href={`/console/shipments/${r.shipment_id}`}>{r.tracking}</Link></td>
                    <td><span className="tag">{r.reason.replaceAll("_", " ")}</span></td>
                    <td><StatusChip state={r.state} /></td>
                    <td className="code">
                      {r.lines.length === 0 ? <span className="muted">parcel not yet received</span> : r.lines.map((l) => (
                        <div key={l.id}>{l.qty} x {l.sku} ({l.condition})</div>
                      ))}
                    </td>
                    <td>
                      {r.lines.map((l) => (
                        <div key={l.id}>{l.state === "dispositioned" ? `${l.decision}${l.destination && l.decision !== "scrap" ? ` to ${l.destination}` : ""}` : "awaiting decision"}</div>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <TableFoot
              count={data.items.length}
              nextHref={data.next_cursor ? `/console/returns?${new URLSearchParams({ ...(q.state ? { state: q.state } : {}), cursor: data.next_cursor })}` : undefined}
              nextLabel="Older returns"
            />
          </div>
        )}
      </div>
    </>
  );
}
