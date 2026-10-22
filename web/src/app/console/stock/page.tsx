import type { Metadata } from "next";
import Link from "next/link";
import { ClockCounterClockwise, Funnel } from "@phosphor-icons/react/ssr";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { TableFoot, Th } from "@/components/Table";
import type { Page, Position } from "@/lib/types";

export const metadata: Metadata = { title: "Stock" };

export default async function StockPage({ searchParams }: { searchParams: Promise<{ sku?: string; location?: string; cursor?: string }> }) {
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const qs = new URLSearchParams({ limit: "100" });
  for (const k of ["sku", "location", "cursor"] as const) if (q[k]) qs.set(k, q[k]!);
  const { data, error } = await load(() => upstream<Page<Position>>(session, "GET", wpath(wid, `/stock?${qs}`)));

  const filterQs = new URLSearchParams();
  if (q.sku) filterQs.set("sku", q.sku);
  if (q.location) filterQs.set("location", q.location);

  const qty = (n: number) => (n === 0 ? "num zero" : "num");

  return (
    <>
      <PageHeader section="stock" title="Stock explorer" />
      <div className="page-body">
        <form className="filters toolbar" role="search">
          <div className="field">
            <label className="label" htmlFor="sku">SKU</label>
            <input id="sku" name="sku" className="code" defaultValue={q.sku} placeholder="SKU-0001" />
          </div>
          <div className="field">
            <label className="label" htmlFor="location">Location</label>
            <input id="location" name="location" className="code" defaultValue={q.location} placeholder="A01-01-A-1" />
          </div>
          <button className="btn secondary" type="submit"><Funnel size={16} aria-hidden="true" />Filter</button>
        </form>
        {error && <Banner>{error}</Banner>}
        {data && data.items.length === 0 && <EmptyState>No stock matches. Receive an ASN or run the generator.</EmptyState>}
        {data && data.items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <Th type="location">Location</Th><Th type="code">SKU</Th><Th type="code">Lot</Th><Th type="code">LPN</Th>
                  <Th type="num">Available</Th><Th type="num">Reserved</Th><Th type="num">In transit</Th>
                  <Th type="num">Quarantined</Th><Th type="num">Damaged</Th><Th type="action" />
                </tr>
              </thead>
              <tbody>
                {data.items.map((p) => (
                  <tr key={`${p.location}|${p.sku}|${p.lot}|${p.lpn}`}>
                    <td className="code">{p.location}</td>
                    <td className="code">{p.sku}</td>
                    <td className="code">{p.lot}</td>
                    <td className="code">{p.lpn || "-"}</td>
                    <td className={qty(p.available)}>{p.available}</td>
                    <td className={qty(p.reserved)}>{p.reserved}</td>
                    <td className={qty(p.in_transit)}>{p.in_transit}</td>
                    <td className={qty(p.quarantined)}>{p.quarantined}</td>
                    <td className={qty(p.damaged)}>{p.damaged}</td>
                    <td>
                      <Link className="btn ghost small" href={`/console/timeline?sku=${encodeURIComponent(p.sku)}&location=${encodeURIComponent(p.location)}`}>
                        <ClockCounterClockwise size={14} aria-hidden="true" />History
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <TableFoot
              count={data.items.length}
              nextHref={data.next_cursor ? `/console/stock?${new URLSearchParams({ ...Object.fromEntries(filterQs), cursor: data.next_cursor })}` : undefined}
            />
          </div>
        )}
      </div>
    </>
  );
}
