import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { ASN, Page } from "@/lib/types";
import { markShort } from "../../actions";

export const metadata: Metadata = { title: "Receiving" };

type Row = { id: string; ref: string; supplier: string; state: string };

export default async function ReceivingPage({ searchParams }: { searchParams: Promise<{ asn?: string; cursor?: string }> }) {
  const q = await searchParams;
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const list = await load(() =>
    upstream<Page<Row>>(session, "GET", wpath(wid, `/asns?limit=50${q.cursor ? `&cursor=${encodeURIComponent(q.cursor)}` : ""}`)));
  const detail = q.asn ? await load(() => upstream<ASN>(session, "GET", wpath(wid, `/asns/${encodeURIComponent(q.asn!)}`))) : null;

  return (
    <>
      <h1>Receiving</h1>
      {list.error && <div className="banner-error" role="alert">{list.error}</div>}
      <div className="cols" style={{ gridTemplateColumns: "minmax(280px, 1fr) minmax(320px, 2fr)" }}>
        <section>
          <h2>Advance shipping notices</h2>
          {list.data?.items.length === 0 && <div className="empty">No ASNs yet. External systems post them with an asn:write token.</div>}
          {list.data && list.data.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Reference</th><th>Supplier</th><th>State</th></tr></thead>
                <tbody>
                  {list.data.items.map((a) => (
                    <tr key={a.id}>
                      <td className="code"><Link href={`/console/receiving?asn=${a.id}`}>{a.ref}</Link></td>
                      <td>{a.supplier || "-"}</td>
                      <td><StatusChip state={a.state} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {list.data?.next_cursor && <p><Link className="btn secondary" href={`/console/receiving?cursor=${encodeURIComponent(list.data.next_cursor)}`}>Next page</Link></p>}
        </section>
        <section>
          {!detail && <div className="empty">Choose an ASN to see expected against received.</div>}
          {detail?.error && <div className="banner-error" role="alert">{detail.error}</div>}
          {detail?.data && (
            <>
              <h2>{detail.data.ref} <StatusChip state={detail.data.state} /></h2>
              <div className="table-wrap">
                <table>
                  <thead><tr><th>SKU</th><th className="num">Expected</th><th className="num">Received</th><th>Result</th><th></th></tr></thead>
                  <tbody>
                    {detail.data.lines.map((l) => {
                      const got = l.received_qty ?? 0;
                      const state = l.short ? "short" : got >= l.expected_qty ? (got > l.expected_qty ? "over" : "complete") : "open";
                      return (
                        <tr key={l.id}>
                          <td className="code">{l.sku}</td>
                          <td className="num">{l.expected_qty}</td>
                          <td className="num">{got}</td>
                          <td>{state === "complete" ? <StatusChip state="completed" /> : state === "open" ? <StatusChip state="open" /> : <StatusChip state="exception" />}
                            <span className="sr-only">{state}</span> {state !== "open" && state !== "complete" ? state : ""}</td>
                          <td>
                            {state === "open" && (
                              <ActionForm action={markShort}>
                                <input type="hidden" name="id" value={l.id} />
                                <SubmitButton className="btn small secondary">Close as short</SubmitButton>
                              </ActionForm>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </section>
      </div>
    </>
  );
}
