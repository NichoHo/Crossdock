import type { Metadata } from "next";
import Link from "next/link";
import { load, requireCtx, wpath } from "@/lib/context";
import { upstream } from "@/lib/upstream";
import { ActionForm, SubmitButton } from "@/components/ActionForm";
import { StatusChip } from "@/components/StatusChip";
import type { AgeBuckets, Page, ReconBreak, Variance, Workbench } from "@/lib/types";
import { confirmVariance, runReconciliation, scheduleCounts, waiveBreak } from "../../actions";

export const metadata: Metadata = { title: "Reconciliation" };

const CLASSES = ["timing", "miscount", "damaged", "shrinkage", "found", "unresolved"];
const KIND_LABEL: Record<string, string> = {
  short_shipped: "Short shipped", over_shipped: "Over shipped", putaway_incomplete: "Putaway incomplete", lot_mismatch: "Lot mismatch",
};

const days = (iso: string) => Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);

function Ageing({ b, title }: { b: AgeBuckets; title: string }) {
  const cells = [
    { n: b.under_7_days, label: "Under 7 days" }, { n: b.d7_to_30, label: "7 to 30 days" },
    { n: b.d30_to_90, label: "30 to 90 days" }, { n: b.over_90, label: "Over 90 days" },
  ];
  return (
    <section aria-label={title}>
      <div className="ageing">
        {cells.map((c, i) => (
          <div key={c.label} className={`b${i}`}>
            <span className="n">{c.n}</span>
            <span className="lbl">{c.label}</span>
          </div>
        ))}
      </div>
    </section>
  );
}

export default async function ReconciliationPage({ searchParams }: { searchParams: Promise<{ tab?: string; cursor?: string }> }) {
  const q = await searchParams;
  const tab = q.tab === "breaks" ? "breaks" : "variances";
  const { session, wid } = await requireCtx({ minRole: "supervisor" });
  const wb = await load(() => upstream<Workbench>(session, "GET", wpath(wid, "/workbench")));
  const qs = new URLSearchParams({ limit: "50" });
  if (q.cursor) qs.set("cursor", q.cursor);
  const vars = tab === "variances" ? await load(() => upstream<Page<Variance>>(session, "GET", wpath(wid, `/variances?${qs}`))) : null;
  const brks = tab === "breaks" ? await load(() => upstream<Page<ReconBreak>>(session, "GET", wpath(wid, `/recon-breaks?${qs}`))) : null;

  return (
    <>
      <h1>Reconciliation workbench</h1>
      {wb.error && <div className="banner-error" role="alert">{wb.error}</div>}
      {wb.data && (
        <>
          <p className="muted" style={{ marginTop: 0 }}>
            {wb.data.open} open item{wb.data.open === 1 ? "" : "s"} by age. The over 90 days number is the one to bring to zero.
          </p>
          <Ageing b={wb.data.total} title="Open variances and breaks by age" />
        </>
      )}

      <div className="row" style={{ marginBottom: 12 }}>
        <ActionForm action={scheduleCounts}><SubmitButton className="btn secondary">Schedule due counts</SubmitButton></ActionForm>
        <ActionForm action={runReconciliation}><SubmitButton className="btn secondary">Reconcile receipts now</SubmitButton></ActionForm>
      </div>

      <nav className="row" aria-label="Workbench sections" style={{ marginBottom: 12 }}>
        <Link className={`btn small ${tab === "variances" ? "" : "secondary"}`} href="/console/reconciliation">
          Count variances{wb.data ? ` (${sum(wb.data.variances)})` : ""}
        </Link>
        <Link className={`btn small ${tab === "breaks" ? "" : "secondary"}`} href="/console/reconciliation?tab=breaks">
          Inbound breaks{wb.data ? ` (${sum(wb.data.breaks)})` : ""}
        </Link>
      </nav>

      {vars?.error && <div className="banner-error" role="alert">{vars.error}</div>}
      {vars?.data && vars.data.items.length === 0 && <div className="empty">No open variances. Counts that agree with the system leave nothing here.</div>}
      {vars?.data && vars.data.items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Location</th><th>SKU / lot</th><th className="num">System</th><th className="num">Counted</th><th className="num">Variance</th>
                <th>Proposed</th><th className="num">Age</th><th>Decision</th>
              </tr>
            </thead>
            <tbody>
              {vars.data.items.map((v) => (
                <tr key={v.id}>
                  <td className="code">{v.location}</td>
                  <td className="code">{v.sku}<br /><span className="muted" style={{ fontSize: 13 }}>{v.lot}{v.lpn ? ` | ${v.lpn}` : ""}</span></td>
                  <td className="num">{v.system_qty}</td>
                  <td className="num">{v.counted_qty}{v.damaged_qty ? ` (${v.damaged_qty} damaged)` : ""}</td>
                  <td className="num">{v.variance > 0 ? `+${v.variance} missing` : v.variance < 0 ? `${-v.variance} extra` : "0"}</td>
                  <td><StatusChip state={v.proposed} />{v.flagged ? <span className="chip" data-tone="stop"> review</span> : null}</td>
                  <td className="num">{days(v.opened_at)} d</td>
                  <td>
                    {v.state === "awaiting_recount" ? (
                      <span className="muted">Waiting for recount <span className="code">{v.recount_task_id}</span></span>
                    ) : (
                      <ActionForm action={confirmVariance}>
                        <input type="hidden" name="id" value={v.id} />
                        <div className="row">
                          <label className="sr-only" htmlFor={`c-${v.id}`}>Classification</label>
                          <select id={`c-${v.id}`} name="classification" defaultValue={v.proposed}>
                            {CLASSES.map((c) => <option key={c} value={c}>{c}</option>)}
                          </select>
                          <label className="sr-only" htmlFor={`n-${v.id}`}>Note</label>
                          <input id={`n-${v.id}`} name="note" placeholder="Note (needed to change)…" autoComplete="off" />
                          <SubmitButton className="btn small">Confirm</SubmitButton>
                        </div>
                      </ActionForm>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {brks?.error && <div className="banner-error" role="alert">{brks.error}</div>}
      {brks?.data && brks.data.items.length === 0 && <div className="empty">No open inbound breaks. ASN, receipt and putaway agree.</div>}
      {brks?.data && brks.data.items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th>Break</th><th>ASN / supplier</th><th>SKU</th><th className="num">ASN</th><th className="num">Received</th><th className="num">Put away</th><th className="num">Age</th><th>Resolve</th></tr>
            </thead>
            <tbody>
              {brks.data.items.map((b) => (
                <tr key={b.id}>
                  <td><span className="chip" data-tone="stop">{KIND_LABEL[b.kind] ?? b.kind}</span><br /><span className="muted" style={{ fontSize: 13 }}>{b.detail}</span></td>
                  <td className="code">{b.asn_ref}<br /><span className="muted" style={{ fontSize: 13 }}>{b.supplier}</span></td>
                  <td className="code">{b.sku}</td>
                  <td className="num">{b.expected_qty}</td>
                  <td className="num">{b.received_qty}</td>
                  <td className="num">{b.putaway_qty}</td>
                  <td className="num">{days(b.opened_at)} d</td>
                  <td>
                    <ActionForm action={waiveBreak}>
                      <input type="hidden" name="id" value={b.id} />
                      <div className="row">
                        <label className="sr-only" htmlFor={`w-${b.id}`}>Note</label>
                        <input id={`w-${b.id}`} name="note" required placeholder="Why accept this…" autoComplete="off" />
                        <SubmitButton className="btn small secondary">Waive</SubmitButton>
                      </div>
                    </ActionForm>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(vars?.data?.next_cursor || brks?.data?.next_cursor) && (
        <p><Link className="btn secondary" href={`/console/reconciliation?${new URLSearchParams({ ...(tab === "breaks" ? { tab } : {}), cursor: (vars?.data?.next_cursor ?? brks?.data?.next_cursor)! })}`}>Next page</Link></p>
      )}
    </>
  );
}

const sum = (b: AgeBuckets) => b.under_7_days + b.d7_to_30 + b.d30_to_90 + b.over_90;
