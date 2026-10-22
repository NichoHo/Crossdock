import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { Banner } from "@/components/Banner";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { StatusChip } from "@/components/StatusChip";
import { TableFoot, Th } from "@/components/Table";
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
      <PageHeader section="receiving" title="Receiving" />
      <div className="page-body">
        {list.error && <Banner>{list.error}</Banner>}
        <div className="split">
          <section>
            <h2>Advance shipping notices</h2>
            {list.data?.items.length === 0 && <EmptyState>No ASNs yet. External systems post them with an asn:write token.</EmptyState>}
            {list.data && list.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead><tr><Th type="code">Reference</Th><Th type="party">Supplier</Th><Th type="status">State</Th></tr></thead>
                  <tbody>
                    {list.data.items.map((a) => (
                      <tr key={a.id} aria-current={q.asn === a.id ? "true" : undefined}>
                        <td className="code"><Link href={`/console/receiving?asn=${a.id}`}>{a.ref}</Link></td>
                        <td>{a.supplier || "-"}</td>
                        <td><StatusChip state={a.state} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <TableFoot
                  count={list.data.items.length}
                  nextHref={list.data.next_cursor ? `/console/receiving?cursor=${encodeURIComponent(list.data.next_cursor)}` : undefined}
                />
              </div>
            )}
          </section>
          <section>
            {!detail && <EmptyState>Choose an ASN to see expected against received.</EmptyState>}
            {detail?.error && <Banner>{detail.error}</Banner>}
            {detail?.data && (
              <>
                <div className="section-head">
                  <h2 className="code">{detail.data.ref}</h2>
                  <StatusChip state={detail.data.state} />
                </div>
                <div className="table-wrap">
                  <table>
                    <thead><tr><Th type="code">SKU</Th><Th type="num">Expected</Th><Th type="num">Received</Th><Th type="status">Result</Th><Th type="action" /></tr></thead>
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
      </div>
    </>
  );
}
