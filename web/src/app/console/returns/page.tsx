import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { StatusChip } from "@/components/StatusChip";
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
      <h1>Returns</h1>
      <nav className="row" aria-label="Filter by state" style={{ marginBottom: 12 }}>
        {STATES.map((s) => (
          <Link key={s} className={`btn small ${(q.state ?? "") === s ? "" : "secondary"}`} href={s ? `/console/returns?state=${s}` : "/console/returns"}>{s || "all"}</Link>
        ))}
      </nav>
      {error && <div className="banner-error" role="alert">{error}</div>}
      {data && data.items.length === 0 && <div className="empty">No returns. A rejected cash-on-delivery parcel opens one automatically.</div>}
      {data && data.items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Return</th><th>Tracking</th><th>Reason</th><th>State</th><th>Units</th><th>Decision</th></tr></thead>
            <tbody>
              {data.items.map((r) => (
                <tr key={r.id}>
                  <td className="code">{r.id}</td>
                  <td className="code"><Link href={`/console/shipments/${r.shipment_id}`}>{r.tracking}</Link></td>
                  <td>{r.reason.replaceAll("_", " ")}</td>
                  <td><StatusChip state={r.state} /></td>
                  <td className="code" style={{ fontSize: 14 }}>
                    {r.lines.length === 0 ? <span className="muted">parcel not yet received</span> : r.lines.map((l) => (
                      <div key={l.id}>{l.qty} x {l.sku} ({l.condition})</div>
                    ))}
                  </td>
                  <td style={{ fontSize: 14 }}>
                    {r.lines.map((l) => (
                      <div key={l.id}>{l.state === "dispositioned" ? `${l.decision}${l.destination && l.decision !== "scrap" ? ` to ${l.destination}` : ""}` : "awaiting decision"}</div>
                    ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data?.next_cursor && <p><Link className="btn secondary" href={`/console/returns?${new URLSearchParams({ ...(q.state ? { state: q.state } : {}), cursor: data.next_cursor })}`}>Older returns</Link></p>}
    </>
  );
}
