import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
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

  return (
    <>
      <h1>Stock explorer</h1>
      <form className="filters" role="search">
        <div className="field">
          <label className="label" htmlFor="sku">SKU</label>
          <input id="sku" name="sku" className="code" defaultValue={q.sku} placeholder="SKU-0001" />
        </div>
        <div className="field">
          <label className="label" htmlFor="location">Location</label>
          <input id="location" name="location" className="code" defaultValue={q.location} placeholder="A01-01-A-1" />
        </div>
        <button className="btn" type="submit">Filter</button>
      </form>
      {error && <div className="banner-error" role="alert">{error}</div>}
      {data && data.items.length === 0 && <div className="empty">No stock matches. Receive an ASN or run the generator.</div>}
      {data && data.items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Location</th><th>SKU</th><th>Lot</th><th>LPN</th>
                <th className="num">Available</th><th className="num">Reserved</th><th className="num">In transit</th>
                <th className="num">Quarantined</th><th className="num">Damaged</th><th></th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((p) => (
                <tr key={`${p.location}|${p.sku}|${p.lot}|${p.lpn}`}>
                  <td className="code">{p.location}</td>
                  <td className="code">{p.sku}</td>
                  <td className="code">{p.lot}</td>
                  <td className="code">{p.lpn || "-"}</td>
                  <td className="num">{p.available}</td>
                  <td className="num">{p.reserved}</td>
                  <td className="num">{p.in_transit}</td>
                  <td className="num">{p.quarantined}</td>
                  <td className="num">{p.damaged}</td>
                  <td><Link href={`/console/timeline?sku=${encodeURIComponent(p.sku)}&location=${encodeURIComponent(p.location)}`}>History</Link></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data?.next_cursor && (
        <p>
          <Link className="btn secondary" href={`/console/stock?${new URLSearchParams({ ...Object.fromEntries(filterQs), cursor: data.next_cursor })}`}>
            Next page
          </Link>
        </p>
      )}
    </>
  );
}
